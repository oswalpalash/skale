package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWritesDesignPartnerFixtureToStdoutAndFile(t *testing.T) {
	t.Parallel()

	outputPath := filepath.Join(t.TempDir(), "fixture.json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"-out", outputPath}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"schemaVersion": "v1alpha1"`) {
		t.Fatalf("expected schema version in stdout, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"forecastSeasonality": "24h0m0s"`) {
		t.Fatalf("expected 24h seasonality in stdout, got %s", stdout.String())
	}

	bytes, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", outputPath, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		t.Fatalf("fixture JSON should decode, got %v", err)
	}
}

func TestRunRejectsUnknownScenario(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"-scenario", "nope"}, &stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("run() exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout, got %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unsupported demo fixture scenario "nope"`) {
		t.Fatalf("expected unsupported scenario message, got %s", stderr.String())
	}
}
