package controller

import (
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/explain"
	"github.com/oswalpalash/skale/internal/safety"
)

const (
	conditionReconciled              = "Reconciled"
	conditionTargetResolved          = "TargetResolved"
	conditionTelemetryReady          = "TelemetryReady"
	conditionRecommendationAvailable = "RecommendationAvailable"

	reasonSucceeded                 = "Succeeded"
	reasonInvalidSpec               = "InvalidSpec"
	reasonUnsupportedMode           = "UnsupportedMode"
	reasonTargetResolved            = "Resolved"
	reasonTargetNotFound            = "TargetNotFound"
	reasonUnsupportedTarget         = "UnsupportedTarget"
	reasonTargetResolutionFailed    = "TargetResolutionFailed"
	reasonTelemetryReady            = "Ready"
	reasonTelemetryDegraded         = "Degraded"
	reasonTelemetryUnsupported      = "Unsupported"
	reasonTelemetryLoadFailed       = "LoadFailed"
	reasonEvaluationFailed          = "EvaluationFailed"
	reasonRecommendationAvailable   = "Available"
	reasonRecommendationSuppressed  = "Suppressed"
	reasonRecommendationUnavailable = "Unavailable"
	reasonNotEvaluated              = "NotEvaluated"
)

type statusBuilder struct {
	projector explain.StatusProjection
}

func (b statusBuilder) markInvalidSpec(status *skalev1alpha1.PredictiveScalingPolicyStatus, generation int64, message string) {
	status.ObservedWorkload = nil
	status.TelemetryReadiness = nil
	status.LastForecast = nil
	status.LastRecommendation = nil
	status.SuppressionReasons = nil

	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionReconciled,
		Status:             metav1.ConditionFalse,
		Reason:             reasonInvalidSpec,
		Message:            message,
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTargetResolved,
		Status:             metav1.ConditionUnknown,
		Reason:             reasonNotEvaluated,
		Message:            "target resolution was skipped because the policy is invalid",
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTelemetryReady,
		Status:             metav1.ConditionUnknown,
		Reason:             reasonNotEvaluated,
		Message:            "telemetry evaluation was skipped because the policy is invalid",
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionRecommendationAvailable,
		Status:             metav1.ConditionUnknown,
		Reason:             reasonNotEvaluated,
		Message:            "recommendation evaluation was skipped because the policy is invalid",
		ObservedGeneration: generation,
	})
}

func (b statusBuilder) markUnsupportedMode(status *skalev1alpha1.PredictiveScalingPolicyStatus, generation int64, message string) {
	status.ObservedWorkload = nil
	status.TelemetryReadiness = nil
	status.LastForecast = nil
	status.LastRecommendation = nil
	status.SuppressionReasons = nil

	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionReconciled,
		Status:             metav1.ConditionFalse,
		Reason:             reasonUnsupportedMode,
		Message:            message,
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTargetResolved,
		Status:             metav1.ConditionUnknown,
		Reason:             reasonNotEvaluated,
		Message:            "target resolution was skipped because the policy mode is unsupported",
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTelemetryReady,
		Status:             metav1.ConditionUnknown,
		Reason:             reasonNotEvaluated,
		Message:            "telemetry evaluation was skipped because the policy mode is unsupported",
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionRecommendationAvailable,
		Status:             metav1.ConditionUnknown,
		Reason:             reasonNotEvaluated,
		Message:            "recommendation evaluation was skipped because the policy mode is unsupported",
		ObservedGeneration: generation,
	})
}

func (b statusBuilder) markTargetFailure(
	status *skalev1alpha1.PredictiveScalingPolicyStatus,
	generation int64,
	reason string,
	message string,
) {
	status.ObservedWorkload = nil
	status.TelemetryReadiness = nil
	status.LastForecast = nil
	status.LastRecommendation = nil
	status.SuppressionReasons = nil

	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionReconciled,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTargetResolved,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTelemetryReady,
		Status:             metav1.ConditionUnknown,
		Reason:             reasonNotEvaluated,
		Message:            "telemetry evaluation was skipped because the target could not be resolved",
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionRecommendationAvailable,
		Status:             metav1.ConditionUnknown,
		Reason:             reasonNotEvaluated,
		Message:            "recommendation evaluation was skipped because the target could not be resolved",
		ObservedGeneration: generation,
	})
}

func (b statusBuilder) markEvaluationFailure(
	status *skalev1alpha1.PredictiveScalingPolicyStatus,
	generation int64,
	message string,
) {
	status.TelemetryReadiness = nil
	status.LastForecast = nil
	status.LastRecommendation = nil
	status.SuppressionReasons = nil

	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionReconciled,
		Status:             metav1.ConditionFalse,
		Reason:             reasonEvaluationFailed,
		Message:            message,
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTelemetryReady,
		Status:             metav1.ConditionUnknown,
		Reason:             reasonNotEvaluated,
		Message:            "telemetry evaluation failed before readiness could be determined",
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionRecommendationAvailable,
		Status:             metav1.ConditionUnknown,
		Reason:             reasonNotEvaluated,
		Message:            "recommendation evaluation failed before a decision could be produced",
		ObservedGeneration: generation,
	})
}

func (b statusBuilder) applyResolvedTarget(
	status *skalev1alpha1.PredictiveScalingPolicyStatus,
	generation int64,
	target ResolvedTarget,
) {
	status.ObservedWorkload = b.projector.ObservedWorkload(target.Identity)
	if status.ObservedWorkload != nil {
		status.ObservedWorkload.APIVersion = target.APIVersion
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTargetResolved,
		Status:             metav1.ConditionTrue,
		Reason:             reasonTargetResolved,
		Message:            target.Message,
		ObservedGeneration: generation,
	})
}

func (b statusBuilder) applyEvaluation(
	status *skalev1alpha1.PredictiveScalingPolicyStatus,
	generation int64,
	evaluation LiveEvaluation,
) {
	status.TelemetryReadiness = b.projector.Telemetry(evaluation.TelemetrySummary)
	status.LastForecast = nil
	status.LastRecommendation = nil
	status.SuppressionReasons = nil

	switch evaluation.TelemetrySummary.State {
	case "ready":
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               conditionTelemetryReady,
			Status:             metav1.ConditionTrue,
			Reason:             reasonTelemetryReady,
			Message:            evaluation.TelemetrySummary.Message,
			ObservedGeneration: generation,
		})
	case "degraded":
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               conditionTelemetryReady,
			Status:             metav1.ConditionFalse,
			Reason:             reasonTelemetryDegraded,
			Message:            evaluation.TelemetrySummary.Message,
			ObservedGeneration: generation,
		})
	default:
		reason := reasonTelemetryUnsupported
		if len(evaluation.TelemetrySummary.BlockingReasons) == 0 && len(evaluation.TelemetrySummary.Reasons) > 0 {
			reason = reasonTelemetryLoadFailed
		}
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               conditionTelemetryReady,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            evaluation.TelemetrySummary.Message,
			ObservedGeneration: generation,
		})
	}

	switch evaluation.Stage {
	case evaluationStageTelemetryUnavailable:
		status.LastRecommendation = unavailableRecommendationSummary(evaluation.EvaluatedAt, evaluation.CurrentReplicas, evaluation.Message)
		status.SuppressionReasons = b.projector.SuppressionReasons(evaluation.SuppressionReason)
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               conditionRecommendationAvailable,
			Status:             metav1.ConditionFalse,
			Reason:             reasonRecommendationUnavailable,
			Message:            evaluation.Message,
			ObservedGeneration: generation,
		})
	case evaluationStageForecastUnavailable:
		status.LastForecast = b.projector.Forecast(derefForecastSummary(evaluation.ForecastSummary))
		status.LastRecommendation = unavailableRecommendationSummary(evaluation.EvaluatedAt, evaluation.CurrentReplicas, evaluation.Message)
		status.SuppressionReasons = b.projector.SuppressionReasons(evaluation.SuppressionReason)
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               conditionRecommendationAvailable,
			Status:             metav1.ConditionFalse,
			Reason:             reasonRecommendationUnavailable,
			Message:            evaluation.Message,
			ObservedGeneration: generation,
		})
	default:
		status.LastForecast = b.projector.Forecast(derefForecastSummary(evaluation.ForecastSummary))
		if evaluation.Recommendation != nil {
			status.LastRecommendation = b.projector.Recommendation(*evaluation.Recommendation)
			status.SuppressionReasons = b.projector.SuppressionReasons(evaluation.Recommendation.Outcome.SuppressionReasons)
			conditionStatus := metav1.ConditionTrue
			conditionReason := reasonRecommendationAvailable
			if evaluation.Recommendation.Outcome.State != string(skalev1alpha1.RecommendationStateAvailable) {
				conditionStatus = metav1.ConditionFalse
				if evaluation.Recommendation.Outcome.State == string(skalev1alpha1.RecommendationStateSuppressed) {
					conditionReason = reasonRecommendationSuppressed
				} else {
					conditionReason = reasonRecommendationUnavailable
				}
			}
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:               conditionRecommendationAvailable,
				Status:             conditionStatus,
				Reason:             conditionReason,
				Message:            evaluation.Recommendation.Outcome.Message,
				ObservedGeneration: generation,
			})
		} else {
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:               conditionRecommendationAvailable,
				Status:             metav1.ConditionUnknown,
				Reason:             reasonNotEvaluated,
				Message:            "recommendation evaluation did not produce a decision",
				ObservedGeneration: generation,
			})
		}
	}

	reconciledMessage := evaluation.Message
	if strings.TrimSpace(reconciledMessage) == "" {
		reconciledMessage = "evaluation completed"
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionReconciled,
		Status:             metav1.ConditionTrue,
		Reason:             reasonSucceeded,
		Message:            reconciledMessage,
		ObservedGeneration: generation,
	})
}

// previousRecommendationFromStatus only returns a prior recommendation when it was
// surfaced for the current spec generation. Older status should not influence
// cooldown decisions after the policy has changed.
func previousRecommendationFromStatus(status skalev1alpha1.PredictiveScalingPolicyStatus, generation int64) *safety.PreviousRecommendation {
	if status.LastRecommendation == nil || status.LastRecommendation.State != skalev1alpha1.RecommendationStateAvailable {
		return nil
	}
	if status.LastRecommendation.EvaluatedAt == nil {
		return nil
	}
	condition := meta.FindStatusCondition(status.Conditions, conditionRecommendationAvailable)
	if condition == nil ||
		condition.ObservedGeneration != generation ||
		condition.Status != metav1.ConditionTrue ||
		condition.Reason != reasonRecommendationAvailable {
		return nil
	}
	return &safety.PreviousRecommendation{
		RecommendedReplicas: status.LastRecommendation.RecommendedReplicas,
		ChangedAt:           status.LastRecommendation.EvaluatedAt.Time.UTC(),
	}
}

func unavailableRecommendationSummary(evaluatedAt time.Time, baselineReplicas int32, message string) *skalev1alpha1.RecommendationSummary {
	metaTime := metav1.NewTime(evaluatedAt.UTC())
	return &skalev1alpha1.RecommendationSummary{
		EvaluatedAt:      &metaTime,
		State:            skalev1alpha1.RecommendationStateUnavailable,
		BaselineReplicas: baselineReplicas,
		Message:          message,
	}
}

func derefForecastSummary(summary *explain.ForecastSummary) explain.ForecastSummary {
	if summary == nil {
		return explain.ForecastSummary{}
	}
	return *summary
}

func nextRequeue(after time.Duration) time.Duration {
	if after <= 0 {
		return time.Minute
	}
	return after
}
