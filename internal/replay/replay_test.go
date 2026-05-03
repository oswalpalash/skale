package replay

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/oswalpalash/skale/internal/explain"
	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/safety"
)

func TestEngineRunReducesOverloadProxyOnRecurringBurst(t *testing.T) {
	t.Parallel()

	engine := Engine{
		Metrics:  staticProvider{snapshot: syntheticSnapshot()},
		Forecast: forecast.SeasonalNaiveModel{},
	}

	result, err := engine.Run(context.Background(), syntheticSpec())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Status != StatusComplete {
		t.Fatalf("status = %q, want %q", result.Status, StatusComplete)
	}
	if result.Baseline.OverloadMinutesProxy <= result.Replay.OverloadMinutesProxy {
		t.Fatalf("expected replay overload proxy %.2f to be lower than baseline %.2f", result.Replay.OverloadMinutesProxy, result.Baseline.OverloadMinutesProxy)
	}
	if len(result.RecommendationEvents) == 0 {
		t.Fatal("expected at least one recommendation event")
	}
	event := result.RecommendationEvents[0]
	if event.Recommendation.RecommendedReplicas != 4 {
		t.Fatalf("recommended replicas = %d, want 4", event.Recommendation.RecommendedReplicas)
	}
	if event.ActivationTime == nil {
		t.Fatal("expected activation time on replay event")
	}
	if event.ActivationTime.Sub(event.EvaluatedAt) != 2*time.Minute {
		t.Fatalf("activation delay = %s, want 2m", event.ActivationTime.Sub(event.EvaluatedAt))
	}
	if event.Workload.Namespace != "payments" || event.Workload.Name != "checkout-api" {
		t.Fatalf("unexpected workload identity %#v", event.Workload)
	}
	if result.Replay.SuppressedCount != 0 {
		t.Fatalf("expected no suppressed evaluations, got %d", result.Replay.SuppressedCount)
	}
}

func TestEngineRunCapturesSuppressionReasonsDuringBlackout(t *testing.T) {
	t.Parallel()

	spec := syntheticSpec()
	spec.Policy.BlackoutWindows = []safety.BlackoutWindow{{
		Name:   "deploy-freeze",
		Start:  spec.Window.Start,
		End:    spec.Window.End,
		Reason: "planned rollout",
	}}

	engine := Engine{
		Metrics:  staticProvider{snapshot: syntheticSnapshot()},
		Forecast: forecast.SeasonalNaiveModel{},
	}

	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Replay.SuppressedCount == 0 {
		t.Fatal("expected suppressed evaluations during blackout")
	}
	if result.Replay.SuppressionReasonCounts[explain.ReasonBlackoutWindowActive] == 0 {
		t.Fatalf("expected blackout suppression count in %#v", result.Replay.SuppressionReasonCounts)
	}
	if len(result.RecommendationEvents) != 0 {
		t.Fatalf("expected blackout window to prevent recommendation events, got %d", len(result.RecommendationEvents))
	}
}

func TestEngineRunCapturesSuppressionReasonsDuringKnownEvent(t *testing.T) {
	t.Parallel()

	spec := syntheticSpec()
	spec.Policy.KnownEvents = []KnownEvent{{
		Name:  "release",
		Start: spec.Window.Start.Add(3 * time.Minute),
		End:   spec.Window.Start.Add(4 * time.Minute),
		Note:  "manual rollout window",
	}}

	engine := Engine{
		Metrics:  staticProvider{snapshot: syntheticSnapshot()},
		Forecast: forecast.SeasonalNaiveModel{},
	}

	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Replay.SuppressedCount == 0 {
		t.Fatal("expected suppressed evaluations during known event window")
	}
	if result.Replay.SuppressionReasonCounts[explain.ReasonBlackoutWindowActive] == 0 {
		t.Fatalf("expected known event suppression count in %#v", result.Replay.SuppressionReasonCounts)
	}
}

func TestEngineRunUsesInjectedHeadroomTimelineForScaleUp(t *testing.T) {
	t.Parallel()

	spec := syntheticSpec()
	spec.Policy.NodeHeadroomMode = safety.NodeHeadroomModeRequireForScaleUp
	spec.Options.HeadroomTimeline = []HeadroomObservation{{
		ObservedAt: spec.Window.Start,
		Signal: safety.NodeHeadroomSignal{
			State:      safety.NodeHeadroomStateReady,
			ObservedAt: spec.Window.Start,
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
		},
	}}

	engine := Engine{
		Metrics:  staticProvider{snapshot: syntheticSnapshot()},
		Forecast: forecast.SeasonalNaiveModel{},
	}

	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Replay.SuppressionReasonCounts[explain.ReasonMissingNodeHeadroom] != 0 {
		t.Fatalf("expected injected headroom to avoid missing-node-headroom suppression, got %#v", result.Replay.SuppressionReasonCounts)
	}
	if len(result.RecommendationEvents) == 0 {
		t.Fatal("expected recommendation events with injected headroom timeline")
	}
}

func TestEngineRunMarksForecastFailuresWithStableSuppressionReason(t *testing.T) {
	t.Parallel()

	engine := Engine{
		Metrics:  staticProvider{snapshot: syntheticSnapshot()},
		Forecast: failingForecastModel{err: errors.New("forecast history is insufficient")},
	}

	result, err := engine.Run(context.Background(), syntheticSpec())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Status != StatusUnsupported {
		t.Fatalf("status = %q, want %q", result.Status, StatusUnsupported)
	}
	if len(result.Evaluations) == 0 {
		t.Fatal("expected replay evaluations even when forecasts fail")
	}
	first := result.Evaluations[0]
	if len(first.SuppressionReasons) != 1 || first.SuppressionReasons[0].Code != explain.ReasonForecastUnavailable {
		t.Fatalf("unexpected suppression reasons %#v", first.SuppressionReasons)
	}
	if result.Replay.SuppressionReasonCounts[explain.ReasonForecastUnavailable] == 0 {
		t.Fatalf("expected forecast_unavailable count in %#v", result.Replay.SuppressionReasonCounts)
	}
}

func TestEngineRunFailsClosedWhenProxyScoringIsUnavailable(t *testing.T) {
	t.Parallel()

	spec := syntheticSpec()
	spec.Options.MinimumCapacitySamples = 1000

	engine := Engine{
		Metrics:  staticProvider{snapshot: syntheticSnapshot()},
		Forecast: forecast.SeasonalNaiveModel{},
	}

	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Status != StatusUnsupported {
		t.Fatalf("status = %q, want %q", result.Status, StatusUnsupported)
	}
	if result.Replay.ScoredMinutes != 0 {
		t.Fatalf("scored minutes = %.2f, want 0", result.Replay.ScoredMinutes)
	}
	if result.Replay.UnscoredMinutes == 0 {
		t.Fatal("expected unscored replay minutes when capacity proxy is unavailable")
	}
	if !containsString(result.UnsupportedReasons, "replay could not estimate required-replica proxy anywhere in the requested window") {
		t.Fatalf("unexpected unsupported reasons %#v", result.UnsupportedReasons)
	}
	if !containsSubstring(result.Caveats, "Outcome proxy scoring was unavailable") {
		t.Fatalf("expected scoring caveat in %#v", result.Caveats)
	}
}

func TestEngineRunSummarizesForecastUnderPrediction(t *testing.T) {
	t.Parallel()

	engine := Engine{
		Metrics: staticProvider{snapshot: syntheticSnapshot()},
		Forecast: qualityForecastModel{validation: forecast.Validation{
			HoldoutPoints:            4,
			UnderPredictedPoints:     2,
			UnderPredictionRate:      0.5,
			MedianUnderPredictionPct: 15,
			UnderPredictionRatios:    []float64{0.10, 0.20},
		}},
	}

	result, err := engine.Run(context.Background(), syntheticSpec())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Replay.ForecastQuality.HoldoutPoints == 0 {
		t.Fatal("expected forecast quality summary")
	}
	if result.Replay.ForecastQuality.UnderPredictionRate != 0.5 {
		t.Fatalf("under-prediction rate = %.2f, want 0.50", result.Replay.ForecastQuality.UnderPredictionRate)
	}
	if math.Abs(result.Replay.ForecastQuality.MedianUnderPredictionPct-15) > 1e-9 {
		t.Fatalf("median under-prediction = %.2f, want 15", result.Replay.ForecastQuality.MedianUnderPredictionPct)
	}
}

type staticProvider struct {
	snapshot metrics.Snapshot
}

func (p staticProvider) LoadWindow(context.Context, metrics.Target, metrics.Window) (metrics.Snapshot, error) {
	return p.snapshot, nil
}

func syntheticSpec() Spec {
	start := time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC)
	end := time.Date(2026, time.April, 2, 0, 29, 0, 0, time.UTC)
	return Spec{
		Target: metrics.Target{
			Namespace: "payments",
			Name:      "checkout-api",
		},
		Window: metrics.Window{
			Start: start,
			End:   end,
		},
		Step:     time.Minute,
		Lookback: 20 * time.Minute,
		Policy: Policy{
			ForecastHorizon:     5 * time.Minute,
			ForecastSeasonality: 10 * time.Minute,
			Warmup:              2 * time.Minute,
			TargetUtilization:   0.8,
			ConfidenceThreshold: 0.7,
			MinReplicas:         2,
			MaxReplicas:         10,
		},
		Options: Options{
			ReadinessOptions: metrics.ReadinessOptions{
				MinimumLookback:                   20 * time.Minute,
				ExpectedResolution:                time.Minute,
				DegradedMissingFraction:           0.10,
				UnsupportedMissingFraction:        0.25,
				DegradedResolutionMultiplier:      2,
				UnsupportedResolutionMultiplier:   4,
				DegradedGapMultiplier:             2,
				UnsupportedGapMultiplier:          4,
				MinimumWarmupSamplesToEstimate:    3,
				DemandStepChangeThreshold:         2.0,
				DegradedDemandUnstableFraction:    0.8,
				UnsupportedDemandUnstableFraction: 1.0,
			},
			CapacityLookback:       15 * time.Minute,
			MinimumCapacitySamples: 3,
		},
	}
}

func syntheticSnapshot() metrics.Snapshot {
	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	step := time.Minute
	demand := repeatPattern([]float64{160, 160, 160, 160, 160, 320, 320, 320, 320, 320}, 3)
	replicas := repeatPattern([]float64{2, 2, 2, 2, 2, 2, 2, 4, 4, 4}, 3)
	cpu := repeatPattern([]float64{0.55, 0.55, 0.55, 0.55, 0.55, 0.82, 0.82, 0.70, 0.70, 0.70}, 3)
	memory := repeatPattern([]float64{0.48, 0.48, 0.48, 0.48, 0.48, 0.60, 0.60, 0.60, 0.60, 0.60}, 3)

	end := start.Add(time.Duration(len(demand)-1) * step)
	return metrics.Snapshot{
		Window: metrics.Window{
			Start: start,
			End:   end,
		},
		Demand:   buildSeries(metrics.SignalDemand, "rps", start, step, demand),
		Replicas: buildSeries(metrics.SignalReplicas, "replicas", start, step, replicas),
		CPU:      seriesPtr(buildSeries(metrics.SignalCPU, "ratio", start, step, cpu)),
		Memory:   seriesPtr(buildSeries(metrics.SignalMemory, "ratio", start, step, memory)),
	}
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

func seriesPtr(series metrics.SignalSeries) *metrics.SignalSeries {
	return &series
}

type failingForecastModel struct {
	err error
}

func (failingForecastModel) Name() string {
	return "failing"
}

func (m failingForecastModel) Forecast(context.Context, forecast.Input) (forecast.Result, error) {
	return forecast.Result{}, m.err
}

type qualityForecastModel struct {
	validation forecast.Validation
}

func (qualityForecastModel) Name() string {
	return "quality"
}

func (m qualityForecastModel) Forecast(_ context.Context, input forecast.Input) (forecast.Result, error) {
	step := input.Step
	if step <= 0 {
		step = time.Minute
	}
	return forecast.Result{
		Model:       "quality",
		GeneratedAt: input.EvaluatedAt,
		Horizon:     input.Horizon,
		Step:        step,
		Points: []forecast.Point{
			{Timestamp: input.EvaluatedAt.Add(2 * time.Minute), Value: 320},
			{Timestamp: input.EvaluatedAt.Add(3 * time.Minute), Value: 320},
		},
		Confidence:  0.9,
		Reliability: forecast.ReliabilityHigh,
		Validation:  m.validation,
	}, nil
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
