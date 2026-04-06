package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/explain"
)

// PredictiveScalingPolicyReconciler hosts the recommendation-first control loop.
type PredictiveScalingPolicyReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Resolver     TargetResolver
	Pipeline     EvaluationPipeline
	Now          func() time.Time
	RequeueAfter time.Duration
}

// Reconcile runs the v1 recommendation-only evaluation flow and writes status, not workload replicas.
func (r *PredictiveScalingPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx).WithValues("predictiveScalingPolicy", req.NamespacedName.String())

	var policy skalev1alpha1.PredictiveScalingPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	builder := statusBuilder{}
	desired := policy.DeepCopy()

	evaluatedPolicy := policy.DeepCopy()
	evaluatedPolicy.Default()
	if validationErrs := evaluatedPolicy.Validate(); len(validationErrs) > 0 {
		message := validationErrs.ToAggregate().Error()
		builder.markInvalidSpec(&desired.Status, policy.Generation, message)
		if err := r.writeStatus(ctx, &policy, desired); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("reconcile completed with invalid policy", "reason", reasonInvalidSpec, "message", message)
		return ctrl.Result{RequeueAfter: nextRequeue(r.RequeueAfter)}, nil
	}

	if evaluatedPolicy.Spec.Mode != skalev1alpha1.PredictiveScalingModeRecommendationOnly {
		message := fmt.Sprintf("mode %q is not supported in v1; only recommendationOnly is allowed", evaluatedPolicy.Spec.Mode)
		builder.markUnsupportedMode(&desired.Status, policy.Generation, message)
		if err := r.writeStatus(ctx, &policy, desired); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("reconcile completed with unsupported mode", "reason", reasonUnsupportedMode, "mode", evaluatedPolicy.Spec.Mode)
		return ctrl.Result{RequeueAfter: nextRequeue(r.RequeueAfter)}, nil
	}

	resolver := r.Resolver
	if resolver == nil {
		resolver = KubernetesTargetResolver{}
	}

	resolvedTarget, err := resolver.Resolve(ctx, r.Client, evaluatedPolicy)
	if err != nil {
		switch {
		case apierrors.IsNotFound(err):
			message := fmt.Sprintf("target workload %s/%s was not found", evaluatedPolicy.Namespace, evaluatedPolicy.Spec.TargetRef.Name)
			builder.markTargetFailure(&desired.Status, policy.Generation, reasonTargetNotFound, message)
			if writeErr := r.writeStatus(ctx, &policy, desired); writeErr != nil {
				return ctrl.Result{}, writeErr
			}
			logger.Info("reconcile completed with missing target", "reason", reasonTargetNotFound, "target", evaluatedPolicy.Spec.TargetRef.Name)
			return ctrl.Result{RequeueAfter: nextRequeue(r.RequeueAfter)}, nil
		case errors.Is(err, ErrUnsupportedTargetRef):
			builder.markTargetFailure(&desired.Status, policy.Generation, reasonUnsupportedTarget, err.Error())
			if writeErr := r.writeStatus(ctx, &policy, desired); writeErr != nil {
				return ctrl.Result{}, writeErr
			}
			logger.Info("reconcile completed with unsupported target", "reason", reasonUnsupportedTarget, "message", err.Error())
			return ctrl.Result{RequeueAfter: nextRequeue(r.RequeueAfter)}, nil
		default:
			builder.markTargetFailure(&desired.Status, policy.Generation, reasonTargetResolutionFailed, err.Error())
			if writeErr := r.writeStatus(ctx, &policy, desired); writeErr != nil {
				return ctrl.Result{}, writeErr
			}
			logger.Error(err, "target resolution failed", "reason", reasonTargetResolutionFailed)
			return ctrl.Result{RequeueAfter: nextRequeue(r.RequeueAfter)}, err
		}
	}
	builder.applyResolvedTarget(&desired.Status, policy.Generation, resolvedTarget)

	evaluation, err := r.Pipeline.Evaluate(ctx, evaluatedPolicy, resolvedTarget, now, previousRecommendationFromStatus(policy.Status, policy.Generation))
	if err != nil {
		builder.markEvaluationFailure(&desired.Status, policy.Generation, err.Error())
		if writeErr := r.writeStatus(ctx, &policy, desired); writeErr != nil {
			return ctrl.Result{}, writeErr
		}
		logger.Error(err, "evaluation pipeline failed", "reason", reasonEvaluationFailed, "target", resolvedTarget.Identity.DisplayName())
		return ctrl.Result{RequeueAfter: nextRequeue(r.RequeueAfter)}, err
	}
	builder.applyEvaluation(&desired.Status, policy.Generation, evaluation)

	if err := r.writeStatus(ctx, &policy, desired); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info(
		"reconciled predictive scaling policy",
		"target", resolvedTarget.Identity.DisplayName(),
		"hpa", resolvedTarget.Identity.HPAName,
		"telemetryState", evaluation.TelemetrySummary.State,
		"forecastMethod", forecastMethod(evaluation),
		"recommendationState", recommendationState(evaluation),
		"recommendedReplicas", recommendedReplicas(evaluation),
		"suppressionReasons", suppressionCodes(evaluation),
	)
	return ctrl.Result{RequeueAfter: nextRequeue(r.RequeueAfter)}, nil
}

func (r *PredictiveScalingPolicyReconciler) writeStatus(
	ctx context.Context,
	current *skalev1alpha1.PredictiveScalingPolicy,
	desired *skalev1alpha1.PredictiveScalingPolicy,
) error {
	if equality.Semantic.DeepEqual(current.Status, desired.Status) {
		return nil
	}

	current.Status = desired.Status
	if err := r.Status().Update(ctx, current); err != nil {
		return fmt.Errorf("update predictive scaling policy status: %w", err)
	}
	return nil
}

// SetupWithManager registers the reconciler with the controller manager.
func (r *PredictiveScalingPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&skalev1alpha1.PredictiveScalingPolicy{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}

func forecastMethod(evaluation LiveEvaluation) string {
	if evaluation.ForecastSummary == nil {
		return ""
	}
	return evaluation.ForecastSummary.Method
}

func recommendationState(evaluation LiveEvaluation) string {
	if evaluation.Recommendation == nil {
		switch evaluation.Stage {
		case evaluationStageForecastUnavailable, evaluationStageTelemetryUnavailable:
			return string(skalev1alpha1.RecommendationStateUnavailable)
		default:
			return ""
		}
	}
	return evaluation.Recommendation.Outcome.State
}

func recommendedReplicas(evaluation LiveEvaluation) int32 {
	if evaluation.Recommendation == nil {
		return 0
	}
	return evaluation.Recommendation.Outcome.FinalRecommendedReplicas
}

func suppressionCodes(evaluation LiveEvaluation) []string {
	if evaluation.Recommendation != nil {
		return explain.ReasonCodes(evaluation.Recommendation.Outcome.SuppressionReasons)
	}
	codes := make([]string, 0, len(evaluation.SuppressionReason))
	for _, reason := range evaluation.SuppressionReason {
		if reason.Code != "" {
			codes = append(codes, reason.Code)
		}
	}
	return codes
}
