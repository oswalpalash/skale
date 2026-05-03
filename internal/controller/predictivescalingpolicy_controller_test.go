package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/explain"
	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/metrics"
)

func TestPredictiveScalingPolicyReconcilerWritesAvailableRecommendation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC)
	scheme := testScheme(t)
	policy := testPolicy()
	policy.Spec.ConfidenceThreshold = 0.4
	policy.Spec.ForecastSeasonality = metav1.Duration{Duration: 5 * time.Minute}
	policy.Spec.NodeHeadroomSanity = skalev1alpha1.NodeHeadroomSanityDisabled

	deployment := testDeployment()
	hpa := testHPA()
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&skalev1alpha1.PredictiveScalingPolicy{}).
		WithObjects(policy.DeepCopy(), deployment.DeepCopy(), hpa.DeepCopy()).
		Build()

	reconciler := PredictiveScalingPolicyReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Resolver: KubernetesTargetResolver{},
		Pipeline: EvaluationPipeline{
			MetricsProvider: slicedProvider{snapshot: syntheticSnapshot()},
			ForecastModel:   forecast.SeasonalNaiveModel{},
		},
		Now:          func() time.Time { return now },
		RequeueAfter: time.Minute,
	}

	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(policy),
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != time.Minute {
		t.Fatalf("RequeueAfter = %s, want 1m", result.RequeueAfter)
	}

	var updated skalev1alpha1.PredictiveScalingPolicy
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(policy), &updated); err != nil {
		t.Fatalf("Get(policy) error = %v", err)
	}

	if updated.Status.ObservedWorkload == nil || updated.Status.ObservedWorkload.Name != "checkout-api" {
		t.Fatalf("unexpected observed workload %#v", updated.Status.ObservedWorkload)
	}
	if updated.Status.ObservedWorkload.HPAName != "checkout-hpa" {
		t.Fatalf("expected HPA name checkout-hpa, got %#v", updated.Status.ObservedWorkload)
	}
	if updated.Status.TelemetryReadiness == nil || updated.Status.TelemetryReadiness.State != skalev1alpha1.TelemetryReadinessStateReady {
		t.Fatalf("unexpected telemetry readiness %#v", updated.Status.TelemetryReadiness)
	}
	if updated.Status.LastForecast == nil || updated.Status.LastForecast.Method != forecast.SeasonalNaiveModelName {
		t.Fatalf("unexpected last forecast %#v", updated.Status.LastForecast)
	}
	if updated.Status.LastRecommendation == nil || updated.Status.LastRecommendation.State != skalev1alpha1.RecommendationStateAvailable {
		t.Fatalf("unexpected recommendation summary %#v", updated.Status.LastRecommendation)
	}
	if updated.Status.LastRecommendation.RecommendedReplicas != 4 {
		t.Fatalf("recommended replicas = %d, want 4", updated.Status.LastRecommendation.RecommendedReplicas)
	}
	if len(updated.Status.SuppressionReasons) != 0 {
		t.Fatalf("expected no suppression reasons, got %#v", updated.Status.SuppressionReasons)
	}

	assertCondition(t, updated.Status.Conditions, conditionReconciled, metav1.ConditionTrue, reasonSucceeded)
	assertCondition(t, updated.Status.Conditions, conditionTargetResolved, metav1.ConditionTrue, reasonTargetResolved)
	assertCondition(t, updated.Status.Conditions, conditionTelemetryReady, metav1.ConditionTrue, reasonTelemetryReady)
	assertCondition(t, updated.Status.Conditions, conditionRecommendationAvailable, metav1.ConditionTrue, reasonRecommendationAvailable)

	var unchanged appsv1.Deployment
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(deployment), &unchanged); err != nil {
		t.Fatalf("Get(deployment) error = %v", err)
	}
	if unchanged.Spec.Replicas == nil || *unchanged.Spec.Replicas != 2 {
		t.Fatalf("deployment replicas changed unexpectedly: %#v", unchanged.Spec.Replicas)
	}
}

func TestPredictiveScalingPolicyReconcilerSuppressesWhenNodeHeadroomRequired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC)
	scheme := testScheme(t)
	policy := testPolicy()
	policy.Spec.ConfidenceThreshold = 0.4
	policy.Spec.ForecastSeasonality = metav1.Duration{Duration: 5 * time.Minute}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&skalev1alpha1.PredictiveScalingPolicy{}).
		WithObjects(policy.DeepCopy(), testDeployment(), testHPA()).
		Build()

	reconciler := PredictiveScalingPolicyReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Resolver: KubernetesTargetResolver{},
		Pipeline: EvaluationPipeline{
			MetricsProvider: slicedProvider{snapshot: syntheticSnapshot()},
			ForecastModel:   forecast.SeasonalNaiveModel{},
		},
		Now: func() time.Time { return now },
	}

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(policy),
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated skalev1alpha1.PredictiveScalingPolicy
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(policy), &updated); err != nil {
		t.Fatalf("Get(policy) error = %v", err)
	}

	if updated.Status.LastRecommendation == nil || updated.Status.LastRecommendation.State != skalev1alpha1.RecommendationStateSuppressed {
		t.Fatalf("unexpected recommendation summary %#v", updated.Status.LastRecommendation)
	}
	if len(updated.Status.SuppressionReasons) == 0 || updated.Status.SuppressionReasons[0].Code != explain.ReasonMissingNodeHeadroom {
		t.Fatalf("unexpected suppression reasons %#v", updated.Status.SuppressionReasons)
	}
	assertCondition(t, updated.Status.Conditions, conditionRecommendationAvailable, metav1.ConditionFalse, reasonRecommendationSuppressed)
}

func TestPredictiveScalingPolicyReconcilerMarksTelemetryLoadFailure(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 0, 25, 0, 0, time.UTC)
	scheme := testScheme(t)
	policy := testPolicy()

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&skalev1alpha1.PredictiveScalingPolicy{}).
		WithObjects(policy.DeepCopy(), testDeployment()).
		Build()

	reconciler := PredictiveScalingPolicyReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Resolver: KubernetesTargetResolver{},
		Pipeline: EvaluationPipeline{
			MetricsProvider: errorProvider{err: errors.New("prometheus is unavailable")},
		},
		Now: func() time.Time { return now },
	}

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(policy),
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated skalev1alpha1.PredictiveScalingPolicy
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(policy), &updated); err != nil {
		t.Fatalf("Get(policy) error = %v", err)
	}

	if updated.Status.TelemetryReadiness == nil || updated.Status.TelemetryReadiness.State != skalev1alpha1.TelemetryReadinessStateUnsupported {
		t.Fatalf("unexpected telemetry readiness %#v", updated.Status.TelemetryReadiness)
	}
	if updated.Status.LastRecommendation == nil || updated.Status.LastRecommendation.State != skalev1alpha1.RecommendationStateUnavailable {
		t.Fatalf("unexpected recommendation summary %#v", updated.Status.LastRecommendation)
	}
	assertCondition(t, updated.Status.Conditions, conditionTelemetryReady, metav1.ConditionFalse, reasonTelemetryLoadFailed)
	assertCondition(t, updated.Status.Conditions, conditionRecommendationAvailable, metav1.ConditionFalse, reasonRecommendationUnavailable)
}

func TestPredictiveScalingPolicyReconcilerMarksMissingTarget(t *testing.T) {
	t.Parallel()

	scheme := testScheme(t)
	policy := testPolicy()
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&skalev1alpha1.PredictiveScalingPolicy{}).
		WithObjects(policy.DeepCopy()).
		Build()

	reconciler := PredictiveScalingPolicyReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Resolver: KubernetesTargetResolver{},
		Now:      func() time.Time { return time.Date(2026, time.April, 2, 0, 25, 0, 0, time.UTC) },
	}

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(policy),
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated skalev1alpha1.PredictiveScalingPolicy
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(policy), &updated); err != nil {
		t.Fatalf("Get(policy) error = %v", err)
	}

	if updated.Status.ObservedWorkload != nil {
		t.Fatalf("expected no observed workload, got %#v", updated.Status.ObservedWorkload)
	}
	assertCondition(t, updated.Status.Conditions, conditionTargetResolved, metav1.ConditionFalse, reasonTargetNotFound)
	assertCondition(t, updated.Status.Conditions, conditionTelemetryReady, metav1.ConditionUnknown, reasonNotEvaluated)
	assertCondition(t, updated.Status.Conditions, conditionRecommendationAvailable, metav1.ConditionUnknown, reasonNotEvaluated)
}

func TestPredictiveScalingPolicyReconcilerIgnoresStalePreviousRecommendationGeneration(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC)
	scheme := testScheme(t)
	policy := testPolicy()
	policy.Generation = 2
	policy.Spec.ConfidenceThreshold = 0.4
	policy.Spec.ForecastSeasonality = metav1.Duration{Duration: 5 * time.Minute}
	policy.Spec.NodeHeadroomSanity = skalev1alpha1.NodeHeadroomSanityDisabled
	lastEvaluated := metav1.NewTime(now.Add(-time.Minute))
	policy.Status.LastRecommendation = &skalev1alpha1.RecommendationSummary{
		EvaluatedAt:         &lastEvaluated,
		State:               skalev1alpha1.RecommendationStateAvailable,
		BaselineReplicas:    2,
		RecommendedReplicas: 3,
		BoundedReplicas:     3,
		Message:             "stale generation recommendation",
	}
	policy.Status.Conditions = []metav1.Condition{{
		Type:               conditionRecommendationAvailable,
		Status:             metav1.ConditionTrue,
		Reason:             reasonRecommendationAvailable,
		Message:            "stale generation recommendation",
		ObservedGeneration: 1,
		LastTransitionTime: metav1.NewTime(now.Add(-time.Minute)),
	}}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&skalev1alpha1.PredictiveScalingPolicy{}).
		WithObjects(policy.DeepCopy(), testDeployment(), testHPA()).
		Build()

	reconciler := PredictiveScalingPolicyReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Resolver: KubernetesTargetResolver{},
		Pipeline: EvaluationPipeline{
			MetricsProvider: slicedProvider{snapshot: syntheticSnapshot()},
			ForecastModel:   forecast.SeasonalNaiveModel{},
		},
		Now: func() time.Time { return now },
	}

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(policy),
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated skalev1alpha1.PredictiveScalingPolicy
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(policy), &updated); err != nil {
		t.Fatalf("Get(policy) error = %v", err)
	}

	if updated.Status.LastRecommendation == nil {
		t.Fatal("expected recommendation summary")
	}
	if updated.Status.LastRecommendation.State != skalev1alpha1.RecommendationStateAvailable {
		t.Fatalf("recommendation state = %q, want available", updated.Status.LastRecommendation.State)
	}
	if updated.Status.LastRecommendation.RecommendedReplicas != 4 {
		t.Fatalf("recommended replicas = %d, want 4", updated.Status.LastRecommendation.RecommendedReplicas)
	}
	for _, reason := range updated.Status.SuppressionReasons {
		if reason.Code == explain.ReasonCooldownActive {
			t.Fatalf("expected stale generation recommendation to be ignored, got suppression reasons %#v", updated.Status.SuppressionReasons)
		}
	}
	assertCondition(t, updated.Status.Conditions, conditionRecommendationAvailable, metav1.ConditionTrue, reasonRecommendationAvailable)
}

func TestPredictiveScalingPolicyReconcilerClearsStaleTelemetryOnEvaluationFailure(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 0, 25, 0, 0, time.UTC)
	scheme := testScheme(t)
	policy := testPolicy()
	policy.Status.TelemetryReadiness = &skalev1alpha1.TelemetryReadinessSummary{
		State:   skalev1alpha1.TelemetryReadinessStateReady,
		Message: "stale telemetry summary",
	}
	policy.Status.LastForecast = &skalev1alpha1.ForecastSummary{
		Method:     forecast.SeasonalNaiveModelName,
		Confidence: 0.9,
		Message:    "stale forecast summary",
	}
	lastEvaluated := metav1.NewTime(now.Add(-2 * time.Minute))
	policy.Status.LastRecommendation = &skalev1alpha1.RecommendationSummary{
		EvaluatedAt:         &lastEvaluated,
		State:               skalev1alpha1.RecommendationStateAvailable,
		RecommendedReplicas: 4,
		Message:             "stale recommendation",
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&skalev1alpha1.PredictiveScalingPolicy{}).
		WithObjects(policy.DeepCopy(), testDeployment(), testHPA()).
		Build()

	reconciler := PredictiveScalingPolicyReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Resolver: KubernetesTargetResolver{},
		Pipeline: EvaluationPipeline{
			MetricsProvider:    slicedProvider{snapshot: syntheticSnapshot()},
			ReadinessEvaluator: failingReadinessEvaluator{err: errors.New("readiness evaluator failed")},
			ForecastModel:      forecast.SeasonalNaiveModel{},
		},
		Now: func() time.Time { return now },
	}

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(policy),
	}); err == nil {
		t.Fatal("expected reconcile to return an evaluation error")
	}

	var updated skalev1alpha1.PredictiveScalingPolicy
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(policy), &updated); err != nil {
		t.Fatalf("Get(policy) error = %v", err)
	}

	if updated.Status.TelemetryReadiness != nil {
		t.Fatalf("expected telemetry summary to be cleared, got %#v", updated.Status.TelemetryReadiness)
	}
	if updated.Status.LastForecast != nil {
		t.Fatalf("expected forecast summary to be cleared, got %#v", updated.Status.LastForecast)
	}
	if updated.Status.LastRecommendation != nil {
		t.Fatalf("expected recommendation summary to be cleared, got %#v", updated.Status.LastRecommendation)
	}
	assertCondition(t, updated.Status.Conditions, conditionReconciled, metav1.ConditionFalse, reasonEvaluationFailed)
	assertCondition(t, updated.Status.Conditions, conditionTelemetryReady, metav1.ConditionUnknown, reasonNotEvaluated)
	assertCondition(t, updated.Status.Conditions, conditionRecommendationAvailable, metav1.ConditionUnknown, reasonNotEvaluated)
}

type slicedProvider struct {
	snapshot metrics.Snapshot
}

func (p slicedProvider) LoadWindow(_ context.Context, _ metrics.Target, window metrics.Window) (metrics.Snapshot, error) {
	return sliceSnapshot(p.snapshot, window), nil
}

type errorProvider struct {
	err error
}

func (p errorProvider) LoadWindow(context.Context, metrics.Target, metrics.Window) (metrics.Snapshot, error) {
	return metrics.Snapshot{}, p.err
}

type failingReadinessEvaluator struct {
	err error
}

func (e failingReadinessEvaluator) Evaluate(metrics.ReadinessInput) (metrics.ReadinessReport, error) {
	return metrics.ReadinessReport{}, e.err
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(clientgoscheme) error = %v", err)
	}
	if err := autoscalingv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(autoscalingv2) error = %v", err)
	}
	if err := skalev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(skalev1alpha1) error = %v", err)
	}
	return scheme
}

func testPolicy() *skalev1alpha1.PredictiveScalingPolicy {
	return &skalev1alpha1.PredictiveScalingPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checkout-policy",
			Namespace: "payments",
		},
		Spec: skalev1alpha1.PredictiveScalingPolicySpec{
			TargetRef: skalev1alpha1.TargetReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "checkout-api",
			},
			Mode:                skalev1alpha1.PredictiveScalingModeRecommendationOnly,
			ForecastHorizon:     metav1.Duration{Duration: 5 * time.Minute},
			TargetUtilization:   0.8,
			Warmup:              skalev1alpha1.WarmupSettings{EstimatedReadyDuration: metav1.Duration{Duration: 45 * time.Second}},
			ConfidenceThreshold: 0.7,
			MinReplicas:         2,
			MaxReplicas:         10,
			CooldownWindow:      metav1.Duration{Duration: 5 * time.Minute},
			NodeHeadroomSanity:  skalev1alpha1.NodeHeadroomSanityRequireForScaleUp,
		},
	}
}

func testDeployment() *appsv1.Deployment {
	replicas := int32(2)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checkout-api",
			Namespace: "payments",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
	}
}

func testHPA() *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checkout-hpa",
			Namespace: "payments",
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "checkout-api",
			},
		},
	}
}

func syntheticSnapshot() metrics.Snapshot {
	start := time.Date(2026, time.April, 1, 23, 50, 0, 0, time.UTC)
	step := 30 * time.Second
	demand := expandToHalfMinute(repeatPattern([]float64{160, 160, 160, 160, 160, 320, 320, 320, 320, 320}, 4))
	replicas := expandToHalfMinute(repeatPattern([]float64{2, 2, 2, 2, 2, 2, 2, 4, 4, 4}, 4))
	cpu := expandToHalfMinute(repeatPattern([]float64{0.55, 0.55, 0.55, 0.55, 0.55, 0.82, 0.82, 0.70, 0.70, 0.70}, 4))
	memory := expandToHalfMinute(repeatPattern([]float64{0.48, 0.48, 0.48, 0.48, 0.48, 0.60, 0.60, 0.60, 0.60, 0.60}, 4))

	end := start.Add(time.Duration(len(demand)-1) * step)
	return metrics.Snapshot{
		Window:   metrics.Window{Start: start, End: end},
		Demand:   buildSeries(metrics.SignalDemand, "rps", start, step, demand),
		Replicas: buildSeries(metrics.SignalReplicas, "replicas", start, step, replicas),
		CPU:      seriesPtr(buildSeries(metrics.SignalCPU, "ratio", start, step, cpu)),
		Memory:   seriesPtr(buildSeries(metrics.SignalMemory, "ratio", start, step, memory)),
	}
}

func coarseSyntheticSnapshot() metrics.Snapshot {
	start := time.Date(2026, time.April, 2, 11, 15, 0, 0, time.UTC)
	step := 5 * time.Minute
	demand := []float64{280, 150, 150, 220, 280, 150, 150, 220, 280, 150, 150, 220, 280, 150, 150, 220, 280, 150, 150}
	replicas := []float64{3, 2, 2, 2, 3, 2, 2, 2, 3, 2, 2, 2, 3, 2, 2, 2, 3, 2, 2}
	cpu := []float64{0.75, 0.60, 0.60, 0.88, 0.75, 0.60, 0.60, 0.88, 0.75, 0.60, 0.60, 0.88, 0.75, 0.60, 0.60, 0.88, 0.75, 0.60, 0.60}
	memory := []float64{0.62, 0.58, 0.58, 0.66, 0.62, 0.58, 0.58, 0.66, 0.62, 0.58, 0.58, 0.66, 0.62, 0.58, 0.58, 0.66, 0.62, 0.58, 0.58}
	warmup := repeatPattern([]float64{600}, len(demand))

	end := start.Add(time.Duration(len(demand)-1) * step)
	return metrics.Snapshot{
		Window:   metrics.Window{Start: start, End: end},
		Demand:   buildSeries(metrics.SignalDemand, "rps", start, step, demand),
		Replicas: buildSeries(metrics.SignalReplicas, "replicas", start, step, replicas),
		CPU:      seriesPtr(buildSeries(metrics.SignalCPU, "ratio", start, step, cpu)),
		Memory:   seriesPtr(buildSeries(metrics.SignalMemory, "ratio", start, step, memory)),
		Warmup:   seriesPtr(buildSeries(metrics.SignalWarmup, "seconds", start, step, warmup)),
	}
}

func sliceSnapshot(snapshot metrics.Snapshot, window metrics.Window) metrics.Snapshot {
	sliced := metrics.Snapshot{
		Window:   window,
		Demand:   sliceSeries(snapshot.Demand, window),
		Replicas: sliceSeries(snapshot.Replicas, window),
	}
	if snapshot.CPU != nil {
		series := sliceSeries(*snapshot.CPU, window)
		sliced.CPU = &series
	}
	if snapshot.Memory != nil {
		series := sliceSeries(*snapshot.Memory, window)
		sliced.Memory = &series
	}
	return sliced
}

func sliceSeries(series metrics.SignalSeries, window metrics.Window) metrics.SignalSeries {
	out := metrics.SignalSeries{
		Name:                    series.Name,
		ObservedLabelSignatures: append([]string(nil), series.ObservedLabelSignatures...),
		Unit:                    series.Unit,
		Samples:                 make([]metrics.Sample, 0, len(series.Samples)),
	}
	for _, sample := range series.Samples {
		if sample.Timestamp.Before(window.Start) || sample.Timestamp.After(window.End) {
			continue
		}
		out.Samples = append(out.Samples, sample)
	}
	return out
}

func buildSeries(name metrics.SignalName, unit string, start time.Time, step time.Duration, values []float64) metrics.SignalSeries {
	samples := make([]metrics.Sample, 0, len(values))
	for index, value := range values {
		samples = append(samples, metrics.Sample{
			Timestamp: start.Add(time.Duration(index) * step),
			Value:     value,
		})
	}
	return metrics.SignalSeries{
		Name:                    name,
		Unit:                    unit,
		ObservedLabelSignatures: []string{"synthetic"},
		Samples:                 samples,
	}
}

func repeatPattern(pattern []float64, repeats int) []float64 {
	out := make([]float64, 0, len(pattern)*repeats)
	for i := 0; i < repeats; i++ {
		out = append(out, pattern...)
	}
	return out
}

func expandToHalfMinute(values []float64) []float64 {
	out := make([]float64, 0, len(values)*2)
	for _, value := range values {
		out = append(out, value, value)
	}
	return out
}

func seriesPtr(series metrics.SignalSeries) *metrics.SignalSeries {
	return &series
}

func assertCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) {
	t.Helper()

	condition := meta.FindStatusCondition(conditions, conditionType)
	if condition == nil {
		t.Fatalf("condition %q not found in %#v", conditionType, conditions)
	}
	if condition.Status != status {
		t.Fatalf("condition %q status = %q, want %q", conditionType, condition.Status, status)
	}
	if condition.Reason != reason {
		t.Fatalf("condition %q reason = %q, want %q", conditionType, condition.Reason, reason)
	}
}
