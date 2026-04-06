package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
)

func TestRunWritesSummaryAndOptionalReports(t *testing.T) {
	t.Parallel()

	inputPath := writeReplayInputFile(t, sampleReplayInputDocument())
	reportDir := t.TempDir()
	jsonPath := filepath.Join(reportDir, "report.json")
	markdownPath := filepath.Join(reportDir, "report.md")
	uiPath := filepath.Join(reportDir, "report.html")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"-input", inputPath, "-json-out", jsonPath, "-markdown-out", markdownPath, "-ui-out", uiPath},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %s", stderr.String())
	}

	summary := stdout.String()
	for _, expected := range []string{
		"Replay summary for payments/checkout-api",
		"status: complete",
		"telemetry:",
		"outcome deltas (replay - baseline):",
		"caveats and limitations:",
	} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("expected %q in summary output:\n%s", expected, summary)
		}
	}

	jsonBytes, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", jsonPath, err)
	}
	if !strings.Contains(string(jsonBytes), `"status": "complete"`) {
		t.Fatalf("expected replay status in JSON output:\n%s", string(jsonBytes))
	}

	markdownBytes, err := os.ReadFile(markdownPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", markdownPath, err)
	}
	markdown := string(markdownBytes)
	for _, expected := range []string{
		"# Replay Report",
		"## Workload Summary",
		"## Caveats and Limitations",
	} {
		if !strings.Contains(markdown, expected) {
			t.Fatalf("expected %q in markdown output:\n%s", expected, markdown)
		}
	}

	uiBytes, err := os.ReadFile(uiPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", uiPath, err)
	}
	ui := string(uiBytes)
	for _, expected := range []string{
		"<title>Skale Replay UI</title>",
		"Lifecycle Timeline",
		"Demand and replica path",
	} {
		if !strings.Contains(ui, expected) {
			t.Fatalf("expected %q in UI output:\n%s", expected, ui)
		}
	}
}

func TestRunSupportsJSONStdout(t *testing.T) {
	t.Parallel()

	inputPath := writeReplayInputFile(t, sampleReplayInputDocument())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"-input", inputPath, "-format", "json"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"recommendationEvents"`) {
		t.Fatalf("expected recommendation events in JSON stdout:\n%s", stdout.String())
	}
}

func TestRunSupportsUIStdout(t *testing.T) {
	t.Parallel()

	inputPath := writeReplayInputFile(t, sampleReplayInputDocument())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"-input", inputPath, "-format", "ui", "-ui-focus", "6m"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %s", stderr.String())
	}
	for _, expected := range []string{
		"<title>Skale Replay UI</title>",
		"Lifecycle Timeline",
		"Chart focuses on 6m around predictive activity inside the replay window",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("expected %q in HTML stdout:\n%s", expected, stdout.String())
		}
	}
}

func TestRunReportsMalformedReplayInput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "bad-replay-input.json")
	if err := os.WriteFile(inputPath, []byte(`{"target":{"name":"checkout-api"},"window":{"start":"2026-04-02T00:20:00Z","end":"2026-04-02T00:29:00Z"},"step":60}`), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", inputPath, err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"-input", inputPath}, &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("run() exit code = %d, want 1", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout, got %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "load replay input:") || !strings.Contains(stderr.String(), "duration must be a JSON string") {
		t.Fatalf("expected malformed input error in stderr, got %s", stderr.String())
	}
}

func TestRunRejectsUnsupportedFormat(t *testing.T) {
	t.Parallel()

	inputPath := writeReplayInputFile(t, sampleReplayInputDocument())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"-input", inputPath, "-format", "yaml"}, &stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("run() exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout, got %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unsupported format "yaml"`) {
		t.Fatalf("expected unsupported format error in stderr, got %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "summary, json, markdown, or ui") {
		t.Fatalf("expected updated format list in stderr, got %s", stderr.String())
	}
}

func writeReplayInputFile(t *testing.T, document replayInputDocument) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "replay-input.json")
	bytes, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(path, bytes, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

func sampleReplayInputDocument() replayInputDocument {
	start := time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC)
	end := time.Date(2026, time.April, 2, 0, 29, 0, 0, time.UTC)
	return replayInputDocument{
		SchemaVersion: "v1alpha1",
		Target: metrics.Target{
			Namespace: "payments",
			Name:      "checkout-api",
		},
		Window: replayWindowDocument{
			Start: start,
			End:   end,
		},
		Step:     durationValue{Duration: time.Minute},
		Lookback: durationValue{Duration: 20 * time.Minute},
		Policy: replayPolicyDocument{
			Workload:            "payments/checkout-api",
			ForecastHorizon:     durationValue{Duration: 5 * time.Minute},
			ForecastSeasonality: durationValue{Duration: 10 * time.Minute},
			Warmup:              durationValue{Duration: 2 * time.Minute},
			TargetUtilization:   0.8,
			ConfidenceThreshold: 0.7,
			MinReplicas:         2,
			MaxReplicas:         10,
		},
		Options: replayOptionsDocument{
			CapacityLookback:       durationValue{Duration: 15 * time.Minute},
			MinimumCapacitySamples: 3,
			Readiness: replayReadinessOptionsDocument{
				MinimumLookback:                   durationValue{Duration: 20 * time.Minute},
				ExpectedResolution:                durationValue{Duration: time.Minute},
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
		},
		Snapshot: sampleReplaySnapshot(),
	}
}

func sampleReplaySnapshot() metrics.Snapshot {
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
