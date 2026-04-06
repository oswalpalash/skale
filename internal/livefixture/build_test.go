package livefixture

import (
	"testing"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
)

func TestBuildDocumentConstructsReplayInputFromObservedSamples(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 3, 12, 0, 0, 0, time.UTC)
	samples := []Sample{
		{Timestamp: start, DemandQPS: 1, ReadyReplicas: 2, CPURatio: 0.25, MemoryRatio: 0.40},
		{Timestamp: start.Add(30 * time.Second), DemandQPS: 4, ReadyReplicas: 2, CPURatio: 0.82, MemoryRatio: 0.52},
		{Timestamp: start.Add(60 * time.Second), DemandQPS: 4, ReadyReplicas: 3, CPURatio: 0.74, MemoryRatio: 0.56},
		{Timestamp: start.Add(90 * time.Second), DemandQPS: 1, ReadyReplicas: 3, CPURatio: 0.30, MemoryRatio: 0.44},
		{Timestamp: start.Add(120 * time.Second), DemandQPS: 1, ReadyReplicas: 2, CPURatio: 0.22, MemoryRatio: 0.40},
	}

	document, err := BuildDocument(Config{
		Target:              target("skale-live-demo", "checkout-api"),
		Workload:            "skale-live-demo/checkout-api",
		Step:                30 * time.Second,
		ReplayDuration:      time.Minute,
		Lookback:            time.Minute,
		ForecastHorizon:     90 * time.Second,
		ForecastSeasonality: 3 * time.Minute,
		Warmup:              90 * time.Second,
		TargetUtilization:   0.8,
		ConfidenceThreshold: 0.65,
		MinReplicas:         2,
		MaxReplicas:         6,
		CooldownWindow:      2 * time.Minute,
	}, samples)
	if err != nil {
		t.Fatalf("BuildDocument() error = %v", err)
	}

	if document.Window.End != samples[len(samples)-1].Timestamp {
		t.Fatalf("window end = %s, want %s", document.Window.End, samples[len(samples)-1].Timestamp)
	}
	if document.Window.Start != document.Window.End.Add(-time.Minute) {
		t.Fatalf("window start = %s, want %s", document.Window.Start, document.Window.End.Add(-time.Minute))
	}
	if len(document.Snapshot.Demand.Samples) != len(samples) {
		t.Fatalf("demand sample count = %d, want %d", len(document.Snapshot.Demand.Samples), len(samples))
	}
	if got := document.Snapshot.Replicas.Samples[2].Value; got != 3 {
		t.Fatalf("replica sample = %.0f, want 3", got)
	}
	if document.Snapshot.CPU == nil || document.Snapshot.Memory == nil || document.Snapshot.Warmup == nil {
		t.Fatal("expected cpu, memory, and warmup series to be present")
	}
	if got := document.Snapshot.Warmup.Samples[0].Value; got != 90 {
		t.Fatalf("warmup sample = %.0f, want 90", got)
	}
}

func TestBuildDocumentRejectsReplayDurationsLongerThanCapture(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 3, 12, 0, 0, 0, time.UTC)
	_, err := BuildDocument(Config{
		Target:         target("skale-live-demo", "checkout-api"),
		Workload:       "skale-live-demo/checkout-api",
		Step:           30 * time.Second,
		ReplayDuration: 4 * time.Minute,
		Lookback:       time.Minute,
	}, []Sample{
		{Timestamp: start, DemandQPS: 1, ReadyReplicas: 2, CPURatio: 0.2, MemoryRatio: 0.4},
		{Timestamp: start.Add(30 * time.Second), DemandQPS: 2, ReadyReplicas: 2, CPURatio: 0.4, MemoryRatio: 0.5},
	})
	if err == nil {
		t.Fatal("BuildDocument() error = nil, want replay duration validation error")
	}
}

func target(namespace, name string) metrics.Target {
	return metrics.Target{Namespace: namespace, Name: name}
}
