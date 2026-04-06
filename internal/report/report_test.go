package report

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oswalpalash/skale/internal/explain"
	"github.com/oswalpalash/skale/internal/replay"
)

func TestJSONWriterSerializesReplayResult(t *testing.T) {
	t.Parallel()

	output, err := JSONWriter{}.Write(context.Background(), sampleResult())
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	text := string(output)
	if !strings.Contains(text, `"status": "complete"`) {
		t.Fatalf("expected serialized status in %s", text)
	}
	if !strings.Contains(text, `"recommendationEvents"`) {
		t.Fatalf("expected recommendation events in %s", text)
	}
}

func TestSummaryWriterRendersConciseOperatorSummary(t *testing.T) {
	t.Parallel()

	output, err := SummaryWriter{}.Write(context.Background(), sampleResult())
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	text := string(output)
	for _, expected := range []string{
		"Replay summary for payments/checkout-api",
		"telemetry: ready - telemetry readiness is sufficient for replay",
		"recommendations: 1 events, 2 available, 1 suppressed, 0 unavailable",
		"outcome deltas (replay - baseline): overload -2.00, excess +1.00",
		"caveats and limitations:",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in summary output:\n%s", expected, text)
		}
	}
}

func TestMarkdownWriterRendersPartnerFacingSections(t *testing.T) {
	t.Parallel()

	output, err := MarkdownWriter{}.Write(context.Background(), sampleResult())
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	text := string(output)
	for _, expected := range []string{
		"# Replay Report",
		"## Workload Summary",
		"## Telemetry Readiness",
		"## Replay Window",
		"## Baseline Summary",
		"## Recommendation Summary",
		"## Suppression Summary",
		"## Outcome Deltas",
		"## Caveats and Limitations",
		"## Recommendation Events",
		"blackout_window_active",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in markdown output:\n%s", expected, text)
		}
	}
}

func TestSummaryWriterRendersUnsupportedReasonsAndDeterministicSuppressionOrdering(t *testing.T) {
	t.Parallel()

	result := sampleResult()
	result.Status = replay.StatusUnsupported
	result.Replay.SuppressionReasonCounts = map[string]int{
		"low_confidence":         1,
		"telemetry_not_ready":    2,
		"blackout_window_active": 2,
	}
	result.UnsupportedReasons = []string{
		"replay could not estimate required-replica proxy anywhere in the requested window",
	}

	output, err := SummaryWriter{}.Write(context.Background(), result)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	text := string(output)
	for _, expected := range []string{
		"status: unsupported",
		"suppression: blackout_window_active=2, telemetry_not_ready=2, low_confidence=1",
		"unsupported reasons:",
		"- replay could not estimate required-replica proxy anywhere in the requested window",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in summary output:\n%s", expected, text)
		}
	}
}

func TestMarkdownWriterRendersUnsupportedFallbackSectionsWhenReplayHasNoEvents(t *testing.T) {
	t.Parallel()

	result := sampleResult()
	result.Status = replay.StatusUnsupported
	result.RecommendationEvents = nil
	result.Caveats = nil
	result.ConfidenceNotes = nil
	result.UnsupportedReasons = []string{
		"replay produced no usable historical evaluations",
	}

	output, err := MarkdownWriter{}.Write(context.Background(), result)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	text := string(output)
	for _, expected := range []string{
		"## Confidence Notes",
		"## Caveats and Limitations",
		"## Recommendation Events",
		"## Unsupported Reasons",
		"- none",
		"- replay produced no usable historical evaluations",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in markdown output:\n%s", expected, text)
		}
	}
}

func TestHTMLWriterRendersFocusedSingleViewTimeline(t *testing.T) {
	t.Parallel()

	output, err := HTMLWriter{FocusWindow: 4 * time.Minute}.Write(context.Background(), sampleResult())
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	text := string(output)
	for _, expected := range []string{
		"<title>Skale Replay UI</title>",
		"Skale replay UI",
		"Lifecycle Timeline",
		"Demand and replica path",
		"timeline-data",
		"Actual replicas",
		"Chart focuses on 4m around predictive activity inside the replay window",
		"2026-04-02T11:55:00Z: 2 -&gt; 4 replicas",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in HTML output:\n%s", expected, text)
		}
	}
}

func TestHTMLWriterFallsBackWhenReplayHasNoEvaluations(t *testing.T) {
	t.Parallel()

	result := sampleResult()
	result.Status = replay.StatusUnsupported
	result.Evaluations = nil
	result.UnsupportedReasons = []string{
		"replay produced no usable historical evaluations",
	}

	output, err := HTMLWriter{}.Write(context.Background(), result)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	text := string(output)
	for _, expected := range []string{
		"Replay timeline unavailable",
		"replay produced no usable historical evaluations",
		"Suppression and Telemetry",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in HTML output:\n%s", expected, text)
		}
	}
}

func TestFullDayTimeTicksUseCoarserSpacingAndDateBoundaryLabels(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 1, 11, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.April, 2, 10, 45, 0, 0, time.UTC)

	spec := selectTimeTickSpec(start, end)
	if spec.Step != 2*time.Hour {
		t.Fatalf("tick step = %s, want 2h", spec.Step)
	}

	if got := formatTimeTickLabel(time.Date(2026, time.April, 1, 14, 0, 0, 0, time.UTC), start, end, spec.Layout); got != "14:00" {
		t.Fatalf("regular full-day tick label = %q, want 14:00", got)
	}
	if got := formatTimeTickLabel(time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC), start, end, spec.Layout); got != "Apr 2 00:00" {
		t.Fatalf("midnight full-day tick label = %q, want Apr 2 00:00", got)
	}
}

func sampleResult() replay.Result {
	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	return replay.Result{
		Status:          replay.StatusComplete,
		GeneratedAt:     now,
		Target:          replay.TargetRef{Namespace: "payments", Name: "checkout-api"},
		Window:          replay.WindowSummary{Start: now.Add(-10 * time.Minute), End: now},
		StepSeconds:     60,
		LookbackSeconds: 1200,
		Policy: replay.PolicySummary{
			Workload:               "payments/checkout-api",
			ForecastHorizonSeconds: 300,
			WarmupSeconds:          120,
			NodeHeadroomMode:       "requireForScaleUp",
		},
		TelemetryReadiness: explain.TelemetryReadinessSummary{
			CheckedAt: now,
			State:     "ready",
			Message:   "telemetry readiness is sufficient for replay",
			Signals: []explain.TelemetrySignalSummary{
				{Name: "demand", State: "ready", Required: true, Message: "demand signal coverage is sufficient"},
				{Name: "replicas", State: "ready", Required: true, Message: "replica signal coverage is sufficient"},
			},
		},
		Baseline: replay.BaselineSummary{
			Mode: replay.SummaryModeObservedReplicas,
			Summary: replay.Summary{
				StartReplicas:              2,
				EndReplicas:                4,
				MinReplicas:                2,
				MaxReplicas:                4,
				MeanReplicas:               2.8,
				ScaleUpEvents:              1,
				OverloadMinutesProxy:       3,
				ExcessHeadroomMinutesProxy: 1,
				ScoredMinutes:              9,
			},
		},
		Replay: replay.ReplaySummary{
			Mode: replay.SummaryModeSimulatedReplay,
			Summary: replay.Summary{
				StartReplicas:              2,
				EndReplicas:                4,
				MinReplicas:                2,
				MaxReplicas:                4,
				MeanReplicas:               3.4,
				ScaleUpEvents:              1,
				OverloadMinutesProxy:       1,
				ExcessHeadroomMinutesProxy: 2,
				ScoredMinutes:              9,
			},
			EvaluationCount:          3,
			AvailableCount:           2,
			SuppressedCount:          1,
			RecommendationEventCount: 1,
			SuppressionReasonCounts: map[string]int{
				"blackout_window_active": 1,
			},
			ForecastModelCounts: map[string]int{
				"seasonal_naive": 3,
			},
			ReliabilityCounts: map[string]int{
				"high": 3,
			},
		},
		RecommendationEvents: []replay.RecommendationEvent{{
			Workload:         explain.WorkloadIdentity{Namespace: "payments", Name: "checkout-api"},
			EvaluatedAt:      now.Add(-5 * time.Minute),
			ActivationTime:   timePtr(now.Add(-3 * time.Minute)),
			BaselineReplicas: 2,
			ReplayReplicas:   2,
			Signals: explain.SignalSummary{
				ObservedAt:      now.Add(-5 * time.Minute),
				CurrentDemand:   160,
				CurrentReplicas: 2,
				WarmupSeconds:   120,
			},
			Forecast: explain.ForecastSummary{
				EvaluatedAt:     now.Add(-5 * time.Minute),
				Method:          "seasonal_naive",
				ForecastFor:     now.Add(-3 * time.Minute),
				PredictedDemand: 320,
				Confidence:      0.92,
			},
			Recommendation: explain.RecommendationSurface{
				State:               "available",
				CurrentReplicas:     2,
				RecommendedReplicas: 4,
				Delta:               2,
				Message:             "seasonal_naive forecast 320.00 for readiness at 2026-04-02T11:57:00Z implied 4 raw replicas; final recommendation 4 replicas.",
			},
			Summary: "seasonal_naive forecast 320.00 for readiness at 2026-04-02T11:57:00Z implied 4 raw replicas; final recommendation 4 replicas.",
		}},
		Evaluations: []replay.Evaluation{
			{
				EvaluatedAt:        now.Add(-7 * time.Minute),
				CurrentDemand:      160,
				BaselineReplicas:   2,
				SimulatedReplicas:  2,
				BaselineOverloaded: false,
				ReplayOverloaded:   false,
				SuppressionReasons: []explain.SuppressionReason{{
					Code:    "blackout_window_active",
					Message: "blackout window is active",
				}},
			},
			{
				EvaluatedAt:        now.Add(-5 * time.Minute),
				CurrentDemand:      160,
				BaselineReplicas:   2,
				SimulatedReplicas:  2,
				BaselineOverloaded: false,
				ReplayOverloaded:   false,
				State:              "available",
				ActivationTime:     timePtr(now.Add(-3 * time.Minute)),
			},
			{
				EvaluatedAt:           now.Add(-3 * time.Minute),
				CurrentDemand:         320,
				BaselineReplicas:      2,
				SimulatedReplicas:     4,
				BaselineOverloaded:    true,
				ReplayOverloaded:      false,
				RequiredReplicasProxy: int32Ptr(4),
				State:                 "available",
			},
		},
		Caveats: []string{
			"Replay uses observed replica behavior as the baseline; it does not reconstruct HPA internals.",
		},
		ConfidenceNotes: []string{
			"Replay forecasts were high reliability for the scored window.",
		},
	}
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func int32Ptr(value int32) *int32 {
	return &value
}
