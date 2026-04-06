package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
)

func TestLoadReplayInputInfersSnapshotWindowFromSeriesBounds(t *testing.T) {
	t.Parallel()

	document := sampleReplayInputDocument()
	document.Snapshot.Window = metrics.Window{}
	expectedStart, expectedEnd, ok := seriesBounds(document.Snapshot)
	if !ok {
		t.Fatal("expected sample snapshot to have series bounds")
	}

	spec, provider, err := loadReplayInput(writeReplayInputFile(t, document))
	if err != nil {
		t.Fatalf("loadReplayInput() error = %v", err)
	}

	static, ok := provider.(staticProvider)
	if !ok {
		t.Fatalf("provider type = %T, want staticProvider", provider)
	}
	if static.snapshot.Window.Start != expectedStart || static.snapshot.Window.End != expectedEnd {
		t.Fatalf("inferred snapshot window = %#v, want start=%s end=%s", static.snapshot.Window, expectedStart, expectedEnd)
	}
	if spec.Window.Start != document.Window.Start || spec.Window.End != document.Window.End {
		t.Fatalf("replay window = %#v, want %#v", spec.Window, document.Window)
	}
}

func TestLoadReplayInputFallsBackToReplayWindowWhenSnapshotHasNoSamples(t *testing.T) {
	t.Parallel()

	document := sampleReplayInputDocument()
	document.Snapshot = metrics.Snapshot{}

	spec, provider, err := loadReplayInput(writeReplayInputFile(t, document))
	if err != nil {
		t.Fatalf("loadReplayInput() error = %v", err)
	}

	static, ok := provider.(staticProvider)
	if !ok {
		t.Fatalf("provider type = %T, want staticProvider", provider)
	}
	expectedWindow := metrics.Window{
		Start: spec.Window.Start.Add(-spec.Lookback),
		End:   spec.Window.End,
	}
	if static.snapshot.Window != expectedWindow {
		t.Fatalf("fallback snapshot window = %#v, want %#v", static.snapshot.Window, expectedWindow)
	}
}

func TestLoadReplayInputRejectsMalformedDurationValue(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "replay-input.json")
	input := `{
  "schemaVersion": "v1alpha1",
  "target": {"namespace": "payments", "name": "checkout-api"},
  "window": {"start": "2026-04-02T00:20:00Z", "end": "2026-04-02T00:29:00Z"},
  "step": 60,
  "lookback": "20m",
  "policy": {
    "workload": "payments/checkout-api",
    "forecastHorizon": "5m",
    "warmup": "2m",
    "minReplicas": 2,
    "maxReplicas": 10
  },
  "snapshot": {}
}`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}

	_, _, err := loadReplayInput(path)
	if err == nil {
		t.Fatal("expected malformed duration input to fail")
	}
	if !strings.Contains(err.Error(), "duration must be a JSON string") {
		t.Fatalf("expected duration parsing error, got %v", err)
	}
}

func TestDurationValueAcceptsBlankStringAsZero(t *testing.T) {
	t.Parallel()

	var value durationValue
	if err := value.UnmarshalJSON([]byte(`"   "`)); err != nil {
		t.Fatalf("UnmarshalJSON() error = %v", err)
	}
	if value.Duration != 0 {
		t.Fatalf("duration = %s, want 0", value.Duration)
	}
}

func TestInferredSnapshotWindowUsesDefaultLookbackWhenReplayLookbackIsZero(t *testing.T) {
	t.Parallel()

	replayWindow := metrics.Window{
		Start: time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC),
		End:   time.Date(2026, time.April, 2, 0, 29, 0, 0, time.UTC),
	}
	window := inferredSnapshotWindow(metrics.Snapshot{}, replayWindow, 0)

	expected := metrics.Window{
		Start: replayWindow.Start.Add(-30 * time.Minute),
		End:   replayWindow.End,
	}
	if window != expected {
		t.Fatalf("window = %#v, want %#v", window, expected)
	}
}
