package main

import (
	"testing"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/replay"
	"github.com/oswalpalash/skale/internal/replayinput"
)

func TestDemoEvaluationTimeUsesStaticSnapshotWindowEnd(t *testing.T) {
	t.Parallel()

	expected := time.Date(2026, time.April, 2, 0, 29, 30, 0, time.UTC)
	evaluationTime, ok := demoEvaluationTime(replayinput.StaticProvider{
		Snapshot: metrics.Snapshot{
			Window: metrics.Window{
				Start: time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC),
				End:   expected,
			},
		},
	})
	if !ok {
		t.Fatal("expected static provider evaluation time")
	}
	if evaluationTime != expected {
		t.Fatalf("evaluation time = %s, want %s", evaluationTime, expected)
	}
}

func TestDemoEvaluationTimeFallsBackToSeriesBounds(t *testing.T) {
	t.Parallel()

	expected := time.Date(2026, time.April, 2, 0, 29, 30, 0, time.UTC)
	evaluationTime, ok := demoEvaluationTime(replayinput.StaticProvider{
		Snapshot: metrics.Snapshot{
			Demand: metrics.SignalSeries{
				Name: metrics.SignalDemand,
				Samples: []metrics.Sample{
					{Timestamp: time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC), Value: 160},
					{Timestamp: expected, Value: 320},
				},
			},
			Replicas: metrics.SignalSeries{
				Name: metrics.SignalReplicas,
				Samples: []metrics.Sample{
					{Timestamp: time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC), Value: 2},
					{Timestamp: expected, Value: 4},
				},
			},
		},
	})
	if !ok {
		t.Fatal("expected derived evaluation time from series bounds")
	}
	if evaluationTime != expected {
		t.Fatalf("evaluation time = %s, want %s", evaluationTime, expected)
	}
}

func TestDemoReadinessExpectedResolutionUsesReplayStep(t *testing.T) {
	t.Parallel()

	spec := replay.Spec{Step: 5 * time.Minute}
	if got := demoReadinessExpectedResolution(spec); got != 5*time.Minute {
		t.Fatalf("demoReadinessExpectedResolution() = %s, want %s", got, 5*time.Minute)
	}
}

func TestDemoForecastSeasonalityUsesReplayPolicy(t *testing.T) {
	t.Parallel()

	spec := replay.Spec{Policy: replay.Policy{ForecastSeasonality: 20 * time.Minute}}
	if got := demoForecastSeasonality(spec); got != 20*time.Minute {
		t.Fatalf("demoForecastSeasonality() = %s, want %s", got, 20*time.Minute)
	}
}
