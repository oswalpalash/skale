package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWritesReplayInputFromCapturedSamples(t *testing.T) {
	t.Parallel()

	inputPath := writeCaptureCSV(t, ""+
		"timestamp,demand_qps,ready_replicas,cpu_ratio,memory_ratio\n"+
		"2026-04-03T12:00:00Z,1.00,2,0.22,0.40\n"+
		"2026-04-03T12:00:30Z,4.00,2,0.84,0.55\n"+
		"2026-04-03T12:01:00Z,4.00,3,0.72,0.57\n"+
		"2026-04-03T12:01:30Z,1.00,3,0.31,0.45\n",
	)
	outPath := filepath.Join(t.TempDir(), "live-replay-input.json")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		[]string{
			"-input", inputPath,
			"-out", outPath,
			"-namespace", "skale-live-demo",
			"-name", "checkout-api",
			"-replay-duration", "1m",
			"-lookback", "1m",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"workload": "skale-live-demo/checkout-api"`) {
		t.Fatalf("expected workload identity in stdout, got %s", stdout.String())
	}

	bytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", outPath, err)
	}
	var decoded replayInputDocument
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		t.Fatalf("replay input should decode, got %v", err)
	}
	if decoded.Window.End.IsZero() || decoded.Window.Start.IsZero() {
		t.Fatalf("expected replay window in generated input, got %#v", decoded.Window)
	}
}

func TestRunRejectsMalformedCaptureCSV(t *testing.T) {
	t.Parallel()

	inputPath := writeCaptureCSV(t, ""+
		"timestamp,demand_qps,ready_replicas,cpu_ratio,memory_ratio\n"+
		"bad,4.00,2,0.84,0.55\n",
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"-input", inputPath}, &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("run() exit code = %d, want 1", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout, got %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "load live samples:") || !strings.Contains(stderr.String(), "parse timestamp") {
		t.Fatalf("expected parse error in stderr, got %s", stderr.String())
	}
}

func writeCaptureCSV(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "capture.csv")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}
