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
	policy.Spec.ForecastContextWindow = metav1.Duration{Duration: 90 * time.Minute}
	policy.Spec.ForecastContextStep = metav1.Duration{Duration: 5 * time.Minute}
	policy.Spec.RecentContextWindow = metav1.Duration{Duration: 90 * time.Minute}
	policy.Spec.RecentContextStep = metav1.Duration{Duration: 5 * time.Minute}
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
	if withoutOverride.Stage != evaluationStageDecision {
		t.Fatalf("without override stage = %q, want %q", withoutOverride.Stage, evaluationStageDecision)
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

func TestEvaluationPipelineUsesTieredForecastContext(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	policy := testPolicy()
	policy.Spec.ForecastContextWindow = metav1.Duration{Duration: 2 * time.Hour}
	policy.Spec.ForecastContextStep = metav1.Duration{Duration: 5 * time.Minute}
	policy.Spec.RecentContextWindow = metav1.Duration{Duration: 35 * time.Minute}
	policy.Spec.RecentContextStep = metav1.Duration{Duration: 30 * time.Second}
	policy.Spec.NodeHeadroomSanity = skalev1alpha1.NodeHeadroomSanityDisabled
	provider := &generatedWindowProvider{}
	model := &capturingForecastModel{}

	result, err := EvaluationPipeline{
		MetricsProvider: provider,
		ForecastModel:   model,
	}.Evaluate(context.Background(), policy, testResolvedTarget(), now, nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if len(provider.windows) != 2 {
		t.Fatalf("loaded windows = %#v, want base and recent windows", provider.windows)
	}
	if provider.windows[0].Step != 5*time.Minute {
		t.Fatalf("base step = %s, want 5m", provider.windows[0].Step)
	}
	if provider.windows[1].Step != 30*time.Second {
		t.Fatalf("recent step = %s, want 30s", provider.windows[1].Step)
	}
	if !provider.windows[0].Start.Equal(now.Add(-2 * time.Hour)) {
		t.Fatalf("base start = %s, want %s", provider.windows[0].Start, now.Add(-2*time.Hour))
	}
	if !provider.windows[1].Start.Equal(now.Add(-35 * time.Minute)) {
		t.Fatalf("recent start = %s, want %s", provider.windows[1].Start, now.Add(-35*time.Minute))
	}
	if model.seriesCount <= 40 {
		t.Fatalf("forecast series count = %d, want merged coarse history plus high-resolution recent tail", model.seriesCount)
	}
	if model.step != 30*time.Second {
		t.Fatalf("forecast step = %s, want recent context step 30s", model.step)
	}
	if !model.firstTimestamp.Equal(now.Add(-2 * time.Hour)) {
		t.Fatalf("forecast first timestamp = %s, want %s", model.firstTimestamp, now.Add(-2*time.Hour))
	}
	if !model.lastTimestamp.Equal(now) {
		t.Fatalf("forecast last timestamp = %s, want %s", model.lastTimestamp, now)
	}
	if result.TelemetrySummary.State != "ready" {
		t.Fatalf("telemetry state = %q, want ready", result.TelemetrySummary.State)
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
	got := controllerForecastSeasonality(spec, 20*time.Minute, nil)
	if got.Period != 20*time.Minute || got.Source != forecast.SeasonalitySourceConfigured {
		t.Fatalf("controllerForecastSeasonality() = %#v, want configured 20m", got)
	}
}

func TestControllerForecastSeasonalityDoesNotDefaultToHorizon(t *testing.T) {
	t.Parallel()

	spec := testPolicy().Spec
	spec.ForecastHorizon.Duration = 10 * time.Minute

	got := controllerForecastSeasonality(spec, 0, nil)
	if got.Period != 0 || got.Source != forecast.SeasonalitySourceNone {
		t.Fatalf("controllerForecastSeasonality() = %#v, want no detected seasonality", got)
	}
}

func TestControllerForecastSeasonalityDetectsEvidenceBackedPeriod(t *testing.T) {
	t.Parallel()

	spec := testPolicy().Spec
	spec.ForecastHorizon.Duration = 5 * time.Minute
	series := signalSeriesToForecastPoints(syntheticSnapshot().Demand)

	got := controllerForecastSeasonality(spec, 0, series)
	if got.Period != 10*time.Minute || got.Source != forecast.SeasonalitySourceDetected {
		t.Fatalf("controllerForecastSeasonality() = %#v, want detected 10m", got)
	}
	if got.Confidence <= 0 {
		t.Fatalf("confidence = %.2f, want positive", got.Confidence)
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

type capturingForecastModel struct {
	seriesCount    int
	firstTimestamp time.Time
	lastTimestamp  time.Time
	step           time.Duration
}

func (m *capturingForecastModel) Name() string {
	return "capturing"
}

func (m *capturingForecastModel) Forecast(_ context.Context, input forecast.Input) (forecast.Result, error) {
	m.seriesCount = len(input.Series)
	m.step = input.Step
	if len(input.Series) > 0 {
		m.firstTimestamp = input.Series[0].Timestamp
		m.lastTimestamp = input.Series[len(input.Series)-1].Timestamp
	}
	return forecast.Result{
		Model:       "capturing",
		GeneratedAt: input.EvaluatedAt,
		Horizon:     input.Horizon,
		Step:        30 * time.Second,
		Points: []forecast.Point{{
			Timestamp: input.EvaluatedAt.Add(time.Minute),
			Value:     180,
		}},
		Confidence:  0.95,
		Reliability: forecast.ReliabilityHigh,
	}, nil
}

type generatedWindowProvider struct {
	windows []metrics.Window
}

func (p *generatedWindowProvider) LoadWindow(_ context.Context, _ metrics.Target, window metrics.Window) (metrics.Snapshot, error) {
	p.windows = append(p.windows, window)
	step := window.Step
	if step <= 0 {
		step = 30 * time.Second
	}
	return metrics.Snapshot{
		Window:   window,
		Demand:   generatedSeries(metrics.SignalDemand, "rps", window, step, 120),
		Replicas: generatedSeries(metrics.SignalReplicas, "replicas", window, step, 3),
		CPU:      generatedSeriesPtr(metrics.SignalCPU, "ratio", window, step, 0.55),
		Memory:   generatedSeriesPtr(metrics.SignalMemory, "ratio", window, step, 0.60),
		Warmup:   generatedSeriesPtr(metrics.SignalWarmup, "seconds", window, step, 45),
	}, nil
}

func generatedSeriesPtr(name metrics.SignalName, unit string, window metrics.Window, step time.Duration, value float64) *metrics.SignalSeries {
	series := generatedSeries(name, unit, window, step, value)
	return &series
}

func generatedSeries(name metrics.SignalName, unit string, window metrics.Window, step time.Duration, value float64) metrics.SignalSeries {
	series := metrics.SignalSeries{
		Name: name,
		Unit: unit,
		ObservedLabelSignatures: []string{
			"deployment=checkout-api,namespace=payments",
		},
	}
	for at := window.Start; !at.After(window.End); at = at.Add(step) {
		series.Samples = append(series.Samples, metrics.Sample{
			Timestamp: at.UTC(),
			Value:     value,
		})
	}
	return series
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
