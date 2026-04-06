package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultEvaluatorSupportedTelemetry(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	knownWarmup := 45 * time.Second

	report, err := evaluator.Evaluate(ReadinessInput{
		EvaluatedAt: now,
		Snapshot: Snapshot{
			Window:   Window{Start: now.Add(-30 * time.Minute), End: now},
			Demand:   series(SignalDemand, now.Add(-30*time.Minute), 61, 30*time.Second, stableDemand),
			Replicas: series(SignalReplicas, now.Add(-30*time.Minute), 61, 30*time.Second, constant(4)),
			CPU:      signalPtr(series(SignalCPU, now.Add(-30*time.Minute), 61, 30*time.Second, alternating(0.55, 0.60))),
			Memory:   signalPtr(series(SignalMemory, now.Add(-30*time.Minute), 61, 30*time.Second, alternating(0.65, 0.68))),
		},
		KnownWarmup: &knownWarmup,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if report.Level != ReadinessLevelSupported {
		t.Fatalf("expected supported, got %q", report.Level)
	}
	if len(report.BlockingReasons) != 0 {
		t.Fatalf("expected no blocking reasons, got %#v", report.BlockingReasons)
	}
	if !strings.Contains(report.Summary, "sufficient") {
		t.Fatalf("expected supported summary, got %q", report.Summary)
	}
	assertSignalLevel(t, report, SignalDemand, SignalLevelSupported)
	assertSignalLevel(t, report, SignalWarmup, SignalLevelSupported)
}

func TestDefaultEvaluatorDegradedWhenWarmupMustBeEstimated(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)

	report, err := evaluator.Evaluate(ReadinessInput{
		EvaluatedAt: now,
		Snapshot: Snapshot{
			Window:   Window{Start: now.Add(-30 * time.Minute), End: now},
			Demand:   series(SignalDemand, now.Add(-30*time.Minute), 61, 30*time.Second, stableDemand),
			Replicas: series(SignalReplicas, now.Add(-30*time.Minute), 61, 30*time.Second, constant(4)),
			CPU:      signalPtr(series(SignalCPU, now.Add(-30*time.Minute), 61, 30*time.Second, alternating(0.55, 0.60))),
			Memory:   signalPtr(series(SignalMemory, now.Add(-30*time.Minute), 61, 30*time.Second, alternating(0.65, 0.68))),
			Warmup:   signalPtr(series(SignalWarmup, now.Add(-5*time.Minute), 6, 1*time.Minute, warmupValues(40, 41, 43, 45, 44, 46))),
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if report.Level != ReadinessLevelDegraded {
		t.Fatalf("expected degraded, got %q", report.Level)
	}
	if len(report.BlockingReasons) != 0 {
		t.Fatalf("expected no blocking reasons, got %#v", report.BlockingReasons)
	}
	assertSignalLevel(t, report, SignalWarmup, SignalLevelDegraded)
	if !strings.Contains(report.Summary, "degraded") {
		t.Fatalf("expected degraded summary, got %q", report.Summary)
	}
}

func TestDefaultEvaluatorUnsupportedWhenRequiredSignalMissing(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	knownWarmup := 45 * time.Second

	report, err := evaluator.Evaluate(ReadinessInput{
		EvaluatedAt: now,
		Snapshot: Snapshot{
			Window:   Window{Start: now.Add(-30 * time.Minute), End: now},
			Replicas: series(SignalReplicas, now.Add(-30*time.Minute), 61, 30*time.Second, constant(4)),
			CPU:      signalPtr(series(SignalCPU, now.Add(-30*time.Minute), 61, 30*time.Second, alternating(0.55, 0.60))),
			Memory:   signalPtr(series(SignalMemory, now.Add(-30*time.Minute), 61, 30*time.Second, alternating(0.65, 0.68))),
		},
		KnownWarmup: &knownWarmup,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if report.Level != ReadinessLevelUnsupported {
		t.Fatalf("expected unsupported, got %q", report.Level)
	}
	assertSignalLevel(t, report, SignalDemand, SignalLevelMissing)
	if len(report.BlockingReasons) == 0 || !strings.Contains(report.BlockingReasons[0], "required signal demand is missing") {
		t.Fatalf("expected blocking reason for missing demand, got %#v", report.BlockingReasons)
	}
}

func TestDefaultEvaluatorUnsupportedForLabelInconsistency(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	knownWarmup := 45 * time.Second

	demand := series(SignalDemand, now.Add(-30*time.Minute), 61, 30*time.Second, stableDemand)
	demand.ObservedLabelSignatures = []string{"service=checkout", "service=checkout,pod=checkout-1"}

	report, err := evaluator.Evaluate(ReadinessInput{
		EvaluatedAt: now,
		Snapshot: Snapshot{
			Window:   Window{Start: now.Add(-30 * time.Minute), End: now},
			Demand:   demand,
			Replicas: series(SignalReplicas, now.Add(-30*time.Minute), 61, 30*time.Second, constant(4)),
			CPU:      signalPtr(series(SignalCPU, now.Add(-30*time.Minute), 61, 30*time.Second, alternating(0.55, 0.60))),
			Memory:   signalPtr(series(SignalMemory, now.Add(-30*time.Minute), 61, 30*time.Second, alternating(0.65, 0.68))),
		},
		KnownWarmup: &knownWarmup,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if report.Level != ReadinessLevelUnsupported {
		t.Fatalf("expected unsupported, got %q", report.Level)
	}
	assertSignalLevel(t, report, SignalDemand, SignalLevelUnsupported)
	if !containsSubstring(report.BlockingReasons, "label signatures") {
		t.Fatalf("expected label inconsistency blocking reason, got %#v", report.BlockingReasons)
	}
}

func TestDefaultEvaluatorUnsupportedForCoarseResolutionAndGaps(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	knownWarmup := 45 * time.Second

	report, err := evaluator.Evaluate(ReadinessInput{
		EvaluatedAt: now,
		Snapshot: Snapshot{
			Window:   Window{Start: now.Add(-30 * time.Minute), End: now},
			Demand:   series(SignalDemand, now.Add(-30*time.Minute), 7, 5*time.Minute, stableDemand),
			Replicas: series(SignalReplicas, now.Add(-30*time.Minute), 7, 5*time.Minute, constant(4)),
			CPU:      signalPtr(series(SignalCPU, now.Add(-30*time.Minute), 7, 5*time.Minute, alternating(0.55, 0.60))),
			Memory:   signalPtr(series(SignalMemory, now.Add(-30*time.Minute), 7, 5*time.Minute, alternating(0.65, 0.68))),
		},
		KnownWarmup: &knownWarmup,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if report.Level != ReadinessLevelUnsupported {
		t.Fatalf("expected unsupported, got %q", report.Level)
	}
	if !containsSubstring(report.BlockingReasons, "median scrape resolution") {
		t.Fatalf("expected coarse resolution reason, got %#v", report.BlockingReasons)
	}
}

func TestDefaultEvaluatorUnsupportedForUnstableDemandSignal(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	knownWarmup := 45 * time.Second

	report, err := evaluator.Evaluate(ReadinessInput{
		EvaluatedAt: now,
		Snapshot: Snapshot{
			Window:   Window{Start: now.Add(-30 * time.Minute), End: now},
			Demand:   series(SignalDemand, now.Add(-30*time.Minute), 61, 30*time.Second, alternating(0, 1000)),
			Replicas: series(SignalReplicas, now.Add(-30*time.Minute), 61, 30*time.Second, constant(4)),
			CPU:      signalPtr(series(SignalCPU, now.Add(-30*time.Minute), 61, 30*time.Second, alternating(0.55, 0.60))),
			Memory:   signalPtr(series(SignalMemory, now.Add(-30*time.Minute), 61, 30*time.Second, alternating(0.65, 0.68))),
		},
		KnownWarmup: &knownWarmup,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if report.Level != ReadinessLevelUnsupported {
		t.Fatalf("expected unsupported, got %q", report.Level)
	}
	assertSignalLevel(t, report, SignalDemand, SignalLevelUnsupported)
	if !containsSubstring(report.BlockingReasons, "changes too abruptly") {
		t.Fatalf("expected unstable demand reason, got %#v", report.BlockingReasons)
	}
}

func assertSignalLevel(t *testing.T, report ReadinessReport, name SignalName, level SignalLevel) {
	t.Helper()
	for _, signal := range report.Signals {
		if signal.Name == name {
			if signal.Level != level {
				t.Fatalf("expected signal %s level %q, got %q", name, level, signal.Level)
			}
			return
		}
	}
	t.Fatalf("signal %s not found in report", name)
}

func containsSubstring(values []string, substring string) bool {
	for _, value := range values {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
}

func series(name SignalName, start time.Time, count int, step time.Duration, generator func(int) float64) SignalSeries {
	samples := make([]Sample, 0, count)
	for i := 0; i < count; i++ {
		samples = append(samples, Sample{
			Timestamp: start.Add(time.Duration(i) * step),
			Value:     generator(i),
		})
	}
	return SignalSeries{
		Name:                    name,
		Samples:                 samples,
		ObservedLabelSignatures: []string{"service=checkout"},
	}
}

func signalPtr(series SignalSeries) *SignalSeries {
	return &series
}

func stableDemand(i int) float64 {
	return 180 + float64(i%5)*10
}

func constant(value float64) func(int) float64 {
	return func(int) float64 { return value }
}

func alternating(a, b float64) func(int) float64 {
	return func(i int) float64 {
		if i%2 == 0 {
			return a
		}
		return b
	}
}

func warmupValues(values ...float64) func(int) float64 {
	return func(i int) float64 {
		if i >= len(values) {
			return values[len(values)-1]
		}
		return values[i]
	}
}
