package replayinput

import (
	"context"
	"testing"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/replay"
	"github.com/oswalpalash/skale/internal/safety"
)

func TestStaticProviderRejectsMismatchedTarget(t *testing.T) {
	t.Parallel()

	provider := StaticProvider{
		Target: metrics.Target{
			Namespace: "payments",
			Name:      "checkout-api",
		},
		Snapshot: metrics.Snapshot{
			Window: metrics.Window{
				Start: time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC),
				End:   time.Date(2026, time.April, 2, 0, 29, 0, 0, time.UTC),
			},
		},
	}

	_, err := provider.LoadWindow(context.Background(), metrics.Target{
		Namespace: "payments",
		Name:      "other-api",
	}, metrics.Window{})
	if err == nil {
		t.Fatal("expected mismatched target error")
	}
}

func TestReplaySpecAndSnapshotInfersSnapshotWindow(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	document := Document{
		Target: metrics.Target{
			Namespace: "payments",
			Name:      "checkout-api",
		},
		Window: WindowDocument{
			Start: start.Add(20 * time.Minute),
			End:   start.Add(29 * time.Minute),
		},
		Lookback: DurationValue{Duration: 20 * time.Minute},
		Snapshot: metrics.Snapshot{
			Demand: metrics.SignalSeries{
				Name: metrics.SignalDemand,
				Samples: []metrics.Sample{
					{Timestamp: start, Value: 160},
					{Timestamp: start.Add(29 * time.Minute), Value: 320},
				},
			},
			Replicas: metrics.SignalSeries{
				Name: metrics.SignalReplicas,
				Samples: []metrics.Sample{
					{Timestamp: start, Value: 2},
					{Timestamp: start.Add(29 * time.Minute), Value: 4},
				},
			},
		},
	}

	_, snapshot := document.ReplaySpecAndSnapshot()
	if snapshot.Window.Start != start || snapshot.Window.End != start.Add(29*time.Minute) {
		t.Fatalf("snapshot window = %#v", snapshot.Window)
	}
}

func TestReplaySpecAndSnapshotPreservesExtendedSafetyInputs(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	document := Document{
		Target: metrics.Target{
			Namespace: "payments",
			Name:      "checkout-api",
		},
		Window: WindowDocument{
			Start: start.Add(20 * time.Minute),
			End:   start.Add(29 * time.Minute),
		},
		Lookback: DurationValue{Duration: 20 * time.Minute},
		Policy: PolicyDocument{
			KnownEvents: []KnownEventDocument{{
				Name:  "release",
				Start: start.Add(24 * time.Minute),
				End:   start.Add(25 * time.Minute),
				Note:  "manual rollout window",
			}},
			DependencyHealth: []DependencyDocument{{
				Name:                "search",
				Healthy:             true,
				HealthyRatio:        0.99,
				MinimumHealthyRatio: 0.95,
				Message:             "healthy",
			}},
		},
		Options: OptionsDocument{
			HeadroomTimeline: []HeadroomObservation{{
				ObservedAt: start.Add(22 * time.Minute),
				Signal: safety.NodeHeadroomSignal{
					State:      safety.NodeHeadroomStateReady,
					ObservedAt: start.Add(22 * time.Minute),
				},
			}},
		},
		Snapshot: metrics.Snapshot{
			Demand: metrics.SignalSeries{
				Name: metrics.SignalDemand,
				Samples: []metrics.Sample{
					{Timestamp: start, Value: 160},
					{Timestamp: start.Add(29 * time.Minute), Value: 320},
				},
			},
			Replicas: metrics.SignalSeries{
				Name: metrics.SignalReplicas,
				Samples: []metrics.Sample{
					{Timestamp: start, Value: 2},
					{Timestamp: start.Add(29 * time.Minute), Value: 4},
				},
			},
		},
	}

	spec, _ := document.ReplaySpecAndSnapshot()
	if len(spec.Policy.KnownEvents) != 1 || spec.Policy.KnownEvents[0].Name != "release" {
		t.Fatalf("unexpected known events %#v", spec.Policy.KnownEvents)
	}
	if len(spec.Policy.DependencyHealth) != 1 || !spec.Policy.DependencyHealth[0].Healthy {
		t.Fatalf("unexpected dependency health %#v", spec.Policy.DependencyHealth)
	}
	if len(spec.Options.HeadroomTimeline) != 1 || spec.Options.HeadroomTimeline[0].Signal.State != safety.NodeHeadroomStateReady {
		t.Fatalf("unexpected headroom timeline %#v", spec.Options.HeadroomTimeline)
	}
	if spec.Options.BaselineMode != replay.BaselineMode("") {
		t.Fatalf("unexpected baseline mode mutation %q", spec.Options.BaselineMode)
	}
}
