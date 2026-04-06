package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/explain"
	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/safety"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEvaluationPipelineReturnsTelemetryUnavailableWhenRequiredSignalMissing(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC)
	policy := testPolicy()
	policy.Spec.NodeHeadroomSanity = skalev1alpha1.NodeHeadroomSanityDisabled
	snapshot := syntheticSnapshot()
	snapshot.CPU = nil

	result, err := EvaluationPipeline{
		MetricsProvider: slicedProvider{snapshot: snapshot},
	}.Evaluate(context.Background(), policy, testResolvedTarget(), now, nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if result.Stage != evaluationStageTelemetryUnavailable {
		t.Fatalf("stage = %q, want %q", result.Stage, evaluationStageTelemetryUnavailable)
	}
	if result.TelemetrySummary.State != "unsupported" {
		t.Fatalf("telemetry state = %q, want unsupported", result.TelemetrySummary.State)
	}
	if result.ForecastSummary != nil {
		t.Fatalf("expected no forecast summary, got %#v", result.ForecastSummary)
	}
	if result.Recommendation != nil {
		t.Fatalf("expected no recommendation, got %#v", result.Recommendation)
	}
	assertSuppressionCode(t, result.SuppressionReason, explain.ReasonTelemetryNotReady)
	if result.Message == "" {
		t.Fatal("expected telemetry failure message")
	}
}

func TestEvaluationPipelineReturnsForecastUnavailableWithStructuredReason(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC)
	policy := testPolicy()
	policy.Spec.NodeHeadroomSanity = skalev1alpha1.NodeHeadroomSanityDisabled

	result, err := EvaluationPipeline{
		MetricsProvider: slicedProvider{snapshot: syntheticSnapshot()},
		ForecastModel:   staticForecastModel{err: errors.New("seasonal history is insufficient")},
	}.Evaluate(context.Background(), policy, testResolvedTarget(), now, nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if result.Stage != evaluationStageForecastUnavailable {
		t.Fatalf("stage = %q, want %q", result.Stage, evaluationStageForecastUnavailable)
	}
	if result.TelemetrySummary.State != "ready" {
		t.Fatalf("telemetry state = %q, want ready", result.TelemetrySummary.State)
	}
	if result.ForecastSummary == nil || result.ForecastSummary.Error != "seasonal history is insufficient" {
		t.Fatalf("unexpected forecast summary %#v", result.ForecastSummary)
	}
	if result.Recommendation != nil {
		t.Fatalf("expected no recommendation, got %#v", result.Recommendation)
	}
	assertSuppressionCode(t, result.SuppressionReason, explain.ReasonForecastUnavailable)
}

func TestEvaluationPipelineSuppressesLowConfidenceRecommendation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC)
	policy := testPolicy()
	policy.Spec.ConfidenceThreshold = 0.7
	policy.Spec.NodeHeadroomSanity = skalev1alpha1.NodeHeadroomSanityDisabled
	warmup := policy.Spec.Warmup.EstimatedReadyDuration.Duration

	result, err := EvaluationPipeline{
		MetricsProvider: slicedProvider{snapshot: syntheticSnapshot()},
		ForecastModel: staticForecastModel{result: forecast.Result{
			Model:       forecast.SeasonalNaiveModelName,
			GeneratedAt: now,
			Horizon:     policy.Spec.ForecastHorizon.Duration,
			Step:        30 * time.Second,
			Points: []forecast.Point{{
				Timestamp: now.Add(warmup),
				Value:     320,
			}},
			Confidence:  0.40,
			Reliability: forecast.ReliabilityLow,
			Validation: forecast.Validation{
				NormalizedError: 0.25,
			},
		}},
	}.Evaluate(context.Background(), policy, testResolvedTarget(), now, nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if result.Stage != evaluationStageDecision {
		t.Fatalf("stage = %q, want %q", result.Stage, evaluationStageDecision)
	}
	if result.ForecastSummary == nil || result.ForecastSummary.Confidence != 0.40 {
		t.Fatalf("unexpected forecast summary %#v", result.ForecastSummary)
	}
	if result.Recommendation == nil {
		t.Fatal("expected recommendation explanation")
	}
	if result.Recommendation.Outcome.State != string(skalev1alpha1.RecommendationStateSuppressed) {
		t.Fatalf("recommendation state = %q, want suppressed", result.Recommendation.Outcome.State)
	}
	assertSuppressionCode(t, result.SuppressionReason, explain.ReasonLowConfidence)
	if result.Recommendation.SafetyChecks.ConfidencePassed {
		t.Fatal("expected confidence safety check to fail")
	}
}

func TestEvaluationPipelineSupportsCoarseDemoTelemetryWithResolutionOverride(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 12, 45, 0, 0, time.UTC)
	policy := testPolicy()
	policy.Spec.ForecastHorizon.Duration = 10 * time.Minute
	policy.Spec.Warmup.EstimatedReadyDuration.Duration = 10 * time.Minute
	policy.Spec.MaxReplicas = 4
	policy.Spec.CooldownWindow.Duration = 10 * time.Minute
	policy.Spec.NodeHeadroomSanity = skalev1alpha1.NodeHeadroomSanityDisabled
	snapshot := coarseSyntheticSnapshot()

	withoutOverride, err := EvaluationPipeline{
		MetricsProvider: slicedProvider{snapshot: snapshot},
	}.Evaluate(context.Background(), policy, testResolvedTarget(), now, nil)
	if err != nil {
		t.Fatalf("Evaluate() without override error = %v", err)
	}
	if withoutOverride.Stage != evaluationStageTelemetryUnavailable {
		t.Fatalf("without override stage = %q, want %q", withoutOverride.Stage, evaluationStageTelemetryUnavailable)
	}

	withOverride, err := EvaluationPipeline{
		MetricsProvider:             slicedProvider{snapshot: snapshot},
		ReadinessExpectedResolution: 5 * time.Minute,
		ForecastSeasonalityOverride: 20 * time.Minute,
	}.Evaluate(context.Background(), policy, testResolvedTarget(), now, nil)
	if err != nil {
		t.Fatalf("Evaluate() with override error = %v", err)
	}
	if withOverride.TelemetrySummary.State != "ready" {
		t.Fatalf("telemetry state = %q, want ready", withOverride.TelemetrySummary.State)
	}
	if withOverride.Stage != evaluationStageDecision {
		t.Fatalf("stage = %q, want %q", withOverride.Stage, evaluationStageDecision)
	}
	if withOverride.Recommendation == nil {
		t.Fatal("expected recommendation explanation")
	}
}

func TestEvaluationPipelineSuppressesDependencyHealthFailure(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC)
	policy := testPolicy()
	policy.Spec.NodeHeadroomSanity = skalev1alpha1.NodeHeadroomSanityDisabled
	policy.Spec.DependencyHealthChecks = []skalev1alpha1.DependencyHealthCheck{{
		Name:            "search",
		Query:           "up",
		MinHealthyRatio: 0.95,
	}}
	warmup := policy.Spec.Warmup.EstimatedReadyDuration.Duration

	result, err := EvaluationPipeline{
		MetricsProvider: slicedProvider{snapshot: syntheticSnapshot()},
		DependencyEvaluator: staticDependencyEvaluator{statuses: []safety.DependencyHealthStatus{{
			Name:                "search",
			Healthy:             false,
			HealthyRatio:        0.72,
			MinimumHealthyRatio: 0.95,
			Message:             `dependency "search" healthy ratio 0.72 is below minimum 0.95`,
		}}},
		ForecastModel: staticForecastModel{result: forecast.Result{
			Model:       forecast.SeasonalNaiveModelName,
			GeneratedAt: now,
			Horizon:     policy.Spec.ForecastHorizon.Duration,
			Step:        30 * time.Second,
			Points: []forecast.Point{{
				Timestamp: now.Add(warmup),
				Value:     320,
			}},
			Confidence:  0.90,
			Reliability: forecast.ReliabilityHigh,
		}},
	}.Evaluate(context.Background(), policy, testResolvedTarget(), now, nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if result.Recommendation == nil {
		t.Fatal("expected recommendation explanation")
	}
	if result.Recommendation.Outcome.State != string(skalev1alpha1.RecommendationStateSuppressed) {
		t.Fatalf("recommendation state = %q, want suppressed", result.Recommendation.Outcome.State)
	}
	assertSuppressionCode(t, result.SuppressionReason, explain.ReasonDependencyHealthFailed)
	if result.Recommendation.SafetyChecks.DependencyHealthPassed == nil || *result.Recommendation.SafetyChecks.DependencyHealthPassed {
		t.Fatalf("expected dependency health failure, got %#v", result.Recommendation.SafetyChecks.DependencyHealthPassed)
	}
}

func TestEvaluationPipelineSuppressesActiveKnownEventWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC)
	policy := testPolicy()
	policy.Spec.NodeHeadroomSanity = skalev1alpha1.NodeHeadroomSanityDisabled
	policy.Spec.KnownEvents = []skalev1alpha1.KnownEvent{{
		Name:  "release",
		Start: metav1.NewTime(now.Add(-time.Minute)),
		End:   metav1.NewTime(now.Add(time.Minute)),
		Note:  "manual rollout window",
	}}
	warmup := policy.Spec.Warmup.EstimatedReadyDuration.Duration

	result, err := EvaluationPipeline{
		MetricsProvider: slicedProvider{snapshot: syntheticSnapshot()},
		ForecastModel: staticForecastModel{result: forecast.Result{
			Model:       forecast.SeasonalNaiveModelName,
			GeneratedAt: now,
			Horizon:     policy.Spec.ForecastHorizon.Duration,
			Step:        30 * time.Second,
			Points: []forecast.Point{{
				Timestamp: now.Add(warmup),
				Value:     320,
			}},
			Confidence:  0.90,
			Reliability: forecast.ReliabilityHigh,
		}},
	}.Evaluate(context.Background(), policy, testResolvedTarget(), now, nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if result.Recommendation == nil {
		t.Fatal("expected recommendation explanation")
	}
	if result.Recommendation.Outcome.State != string(skalev1alpha1.RecommendationStateSuppressed) {
		t.Fatalf("recommendation state = %q, want suppressed", result.Recommendation.Outcome.State)
	}
	assertSuppressionCode(t, result.SuppressionReason, explain.ReasonBlackoutWindowActive)
}

func TestEvaluationPipelineUsesProvidedNodeHeadroomForScaleUp(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC)
	policy := testPolicy()
	policy.Spec.ConfidenceThreshold = 0.4
	warmup := policy.Spec.Warmup.EstimatedReadyDuration.Duration

	result, err := EvaluationPipeline{
		MetricsProvider:  slicedProvider{snapshot: syntheticSnapshot()},
		HeadroomProvider: staticHeadroomProvider{signal: sufficientHeadroomSignal(now)},
		ForecastModel: staticForecastModel{result: forecast.Result{
			Model:       forecast.SeasonalNaiveModelName,
			GeneratedAt: now,
			Horizon:     policy.Spec.ForecastHorizon.Duration,
			Step:        30 * time.Second,
			Points: []forecast.Point{{
				Timestamp: now.Add(warmup),
				Value:     320,
			}},
			Confidence:  0.90,
			Reliability: forecast.ReliabilityHigh,
		}},
	}.Evaluate(context.Background(), policy, testResolvedTarget(), now, nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if result.Recommendation == nil {
		t.Fatal("expected recommendation explanation")
	}
	if result.Recommendation.Outcome.State != string(skalev1alpha1.RecommendationStateAvailable) {
		t.Fatalf("recommendation state = %q, want available", result.Recommendation.Outcome.State)
	}
	if result.Recommendation.SafetyChecks.NodeHeadroomPassed == nil || !*result.Recommendation.SafetyChecks.NodeHeadroomPassed {
		t.Fatalf("expected node headroom to pass, got %#v", result.Recommendation.SafetyChecks.NodeHeadroomPassed)
	}
}

func TestControllerForecastSeasonalityUsesOverride(t *testing.T) {
	t.Parallel()

	spec := testPolicy().Spec
	spec.ForecastHorizon.Duration = 10 * time.Minute
	if got := controllerForecastSeasonality(spec, 20*time.Minute); got != 20*time.Minute {
		t.Fatalf("controllerForecastSeasonality() = %s, want %s", got, 20*time.Minute)
	}
}

type staticForecastModel struct {
	result forecast.Result
	err    error
}

func (m staticForecastModel) Name() string {
	if m.result.Model != "" {
		return m.result.Model
	}
	return "static"
}

func (m staticForecastModel) Forecast(context.Context, forecast.Input) (forecast.Result, error) {
	if m.err != nil {
		return forecast.Result{}, m.err
	}
	return m.result, nil
}

func testResolvedTarget() ResolvedTarget {
	return ResolvedTarget{
		Identity: explain.WorkloadIdentity{
			Namespace: "payments",
			Name:      "checkout-api",
			Kind:      "Deployment",
			Resource:  "payments/checkout-api",
			HPAName:   "checkout-hpa",
		},
		APIVersion: "apps/v1",
		Message:    "resolved Deployment payments/checkout-api; resolved HPA \"checkout-hpa\" for the target Deployment",
	}
}

func assertSuppressionCode(t *testing.T, reasons []explain.SuppressionReason, code string) {
	t.Helper()

	for _, reason := range reasons {
		if reason.Code == code {
			return
		}
	}
	t.Fatalf("expected suppression code %q in %#v", code, reasons)
}

type staticDependencyEvaluator struct {
	statuses []safety.DependencyHealthStatus
	err      error
}

func (e staticDependencyEvaluator) Evaluate(context.Context, metrics.Target, []skalev1alpha1.DependencyHealthCheck, time.Time) ([]safety.DependencyHealthStatus, error) {
	if e.err != nil {
		return nil, e.err
	}
	return append([]safety.DependencyHealthStatus(nil), e.statuses...), nil
}

type staticHeadroomProvider struct {
	signal *safety.NodeHeadroomSignal
	err    error
}

func (p staticHeadroomProvider) HeadroomFor(context.Context, ResolvedTarget, time.Time) (*safety.NodeHeadroomSignal, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.signal, nil
}

func sufficientHeadroomSignal(observedAt time.Time) *safety.NodeHeadroomSignal {
	return &safety.NodeHeadroomSignal{
		State:      safety.NodeHeadroomStateReady,
		ObservedAt: observedAt,
		PodRequests: safety.Resources{
			CPUMilli:    250,
			MemoryBytes: 256 * 1024 * 1024,
		},
		ClusterSummary: safety.AllocatableSummary{
			Allocatable: safety.Resources{
				CPUMilli:    8000,
				MemoryBytes: 16 * 1024 * 1024 * 1024,
			},
			Requested: safety.Resources{
				CPUMilli:    3000,
				MemoryBytes: 6 * 1024 * 1024 * 1024,
			},
		},
		Nodes: []safety.NodeAllocatableSummary{{
			Name:        "node-a",
			Schedulable: true,
			Summary: safety.AllocatableSummary{
				Allocatable: safety.Resources{
					CPUMilli:    4000,
					MemoryBytes: 8 * 1024 * 1024 * 1024,
				},
				Requested: safety.Resources{
					CPUMilli:    1500,
					MemoryBytes: 3 * 1024 * 1024 * 1024,
				},
			},
		}},
	}
}
