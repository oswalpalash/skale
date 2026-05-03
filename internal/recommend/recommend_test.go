package recommend

import (
	"strings"
	"testing"
	"time"

	"github.com/oswalpalash/skale/internal/explain"
	"github.com/oswalpalash/skale/internal/safety"
)

func TestDeterministicEngineRecommendScaleUp(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.CurrentDemand = 200
	input.CurrentReplicas = 4
	input.ForecastedDemand = 320

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.State != StateAvailable {
		t.Fatalf("expected available state, got %q", result.State)
	}
	if result.RawRequiredReplicas != 6 {
		t.Fatalf("expected raw required replicas 6, got %d", result.RawRequiredReplicas)
	}
	if result.FinalRecommendedReplicas != 6 {
		t.Fatalf("expected final recommended replicas 6, got %d", result.FinalRecommendedReplicas)
	}
	if result.Delta != 2 {
		t.Fatalf("expected delta 2, got %d", result.Delta)
	}
	if result.Explanation.Derived.RawRequiredReplicas != 6 {
		t.Fatalf("expected explanation raw replicas 6, got %d", result.Explanation.Derived.RawRequiredReplicas)
	}
	if !strings.Contains(result.Explanation.Summary, "final recommendation 6 replicas") {
		t.Fatalf("expected summary to mention final recommendation, got %q", result.Explanation.Summary)
	}
}

func TestDeterministicEngineUsesStableCapacityEstimate(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.CurrentDemand = 80
	input.CurrentReplicas = 4
	input.ForecastedDemand = 320
	input.CapacityEstimate = &CapacityEstimate{
		Estimated:          true,
		PerReplicaCapacity: 100,
		WindowStart:        input.EvaluationTime.Add(-15 * time.Minute),
		WindowEnd:          input.EvaluationTime,
		SampleCount:        8,
	}

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.RawRequiredReplicas != 4 {
		t.Fatalf("expected raw required replicas 4 from stable capacity, got %d", result.RawRequiredReplicas)
	}
	if result.Explanation.Signals.CapacitySampleCount != 8 {
		t.Fatalf("capacity samples = %d, want 8", result.Explanation.Signals.CapacitySampleCount)
	}
	if !result.Explanation.Signals.CapacityWindowStart.Equal(input.CapacityEstimate.WindowStart) {
		t.Fatalf("capacity window start = %s, want %s", result.Explanation.Signals.CapacityWindowStart, input.CapacityEstimate.WindowStart)
	}
}

func TestDeterministicEngineAppliesStepUpBound(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.CurrentDemand = 200
	input.CurrentReplicas = 4
	input.ForecastedDemand = 700
	input.MaxReplicas = 20
	input.MaxStepUp = int32Ptr(3)

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.State != StateAvailable {
		t.Fatalf("expected available state, got %q", result.State)
	}
	if result.RawRequiredReplicas != 12 {
		t.Fatalf("expected raw replicas 12, got %d", result.RawRequiredReplicas)
	}
	if result.FinalRecommendedReplicas != 7 {
		t.Fatalf("expected step-bounded final replicas 7, got %d", result.FinalRecommendedReplicas)
	}
	if !result.Explanation.BoundsApplied.StepUpBounded {
		t.Fatal("expected explanation to record step-up bounding")
	}
	assertBoundDetailCode(t, result.Explanation.BoundsApplied.Details, "step_up")
}

func TestDeterministicEngineAppliesStepDownBound(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.CurrentDemand = 400
	input.CurrentReplicas = 8
	input.ForecastedDemand = 100
	input.MinReplicas = 1
	input.MaxReplicas = 20
	input.MaxStepDown = int32Ptr(2)

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.State != StateAvailable {
		t.Fatalf("expected available state, got %q", result.State)
	}
	if result.RawRequiredReplicas != 2 {
		t.Fatalf("expected raw replicas 2, got %d", result.RawRequiredReplicas)
	}
	if result.FinalRecommendedReplicas != 6 {
		t.Fatalf("expected step-bounded final replicas 6, got %d", result.FinalRecommendedReplicas)
	}
	if !result.Explanation.BoundsApplied.StepDownBounded {
		t.Fatal("expected explanation to record step-down bounding")
	}
}

func TestDeterministicEngineSuppressesLowConfidenceButKeepsCandidate(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.CurrentDemand = 200
	input.CurrentReplicas = 4
	input.ForecastedDemand = 320
	input.ConfidenceScore = 0.6
	input.ConfidenceThreshold = 0.7

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.State != StateSuppressed {
		t.Fatalf("expected suppressed state, got %q", result.State)
	}
	if !result.Suppressed {
		t.Fatal("expected suppressed flag to be true")
	}
	if result.FinalRecommendedReplicas != 6 {
		t.Fatalf("expected candidate replicas 6 to be preserved, got %d", result.FinalRecommendedReplicas)
	}
	assertReasonCode(t, result.SuppressionReasons, explain.ReasonLowConfidence)
	if result.Explanation.SafetyChecks.ConfidencePassed {
		t.Fatal("expected explanation to record failed confidence check")
	}
}

func TestDeterministicEngineSuppressesTelemetryNotReady(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.Telemetry = &safety.TelemetryStatus{
		Level:   safety.TelemetryLevelDegraded,
		Reasons: []string{"warmup signal is missing"},
	}

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.State != StateSuppressed {
		t.Fatalf("expected suppressed state, got %q", result.State)
	}
	assertReasonCode(t, result.SuppressionReasons, explain.ReasonTelemetryNotReady)
	if result.Explanation.SafetyChecks.TelemetryReady == nil || *result.Explanation.SafetyChecks.TelemetryReady {
		t.Fatal("expected explanation to record telemetry readiness failure")
	}
}

func TestDeterministicEngineSuppressesOperatorPause(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.OperatorMode = safety.OperatorModePaused

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.State != StateSuppressed {
		t.Fatalf("expected suppressed state, got %q", result.State)
	}
	assertReasonCode(t, result.SuppressionReasons, explain.ReasonOperatorPaused)
	if result.Explanation.SafetyChecks.OperatorMode != string(safety.OperatorModePaused) {
		t.Fatalf("expected operator mode %q in explanation, got %q", safety.OperatorModePaused, result.Explanation.SafetyChecks.OperatorMode)
	}
	if result.Explanation.SafetyChecks.OperatorEnabled {
		t.Fatal("expected explanation to record operator disabled state")
	}
}

func TestDeterministicEngineSuppressesCooldownChange(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.CurrentDemand = 200
	input.CurrentReplicas = 4
	input.ForecastedDemand = 700
	input.MaxReplicas = 20
	input.LastRecommendation = &safety.PreviousRecommendation{
		RecommendedReplicas: 6,
		ChangedAt:           input.EvaluationTime.Add(-2 * time.Minute),
	}
	input.CooldownWindow = 5 * time.Minute

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.State != StateSuppressed {
		t.Fatalf("expected suppressed state, got %q", result.State)
	}
	assertReasonCode(t, result.SuppressionReasons, explain.ReasonCooldownActive)
	if result.Explanation.SafetyChecks.CooldownPassed {
		t.Fatal("expected explanation to record failed cooldown check")
	}
}

func TestDeterministicEngineSuppressesInsufficientNodeHeadroom(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.CurrentDemand = 200
	input.CurrentReplicas = 4
	input.ForecastedDemand = 320
	input.NodeHeadroomMode = safety.NodeHeadroomModeRequireForScaleUp
	input.NodeHeadroom = &safety.NodeHeadroomSignal{
		State:      safety.NodeHeadroomStateReady,
		ObservedAt: input.EvaluationTime,
		PodRequests: safety.Resources{
			CPUMilli:    500,
			MemoryBytes: 1073741824,
		},
		ClusterSummary: safety.AllocatableSummary{
			Allocatable: safety.Resources{
				CPUMilli:    16000,
				MemoryBytes: 34359738368,
			},
			Requested: safety.Resources{
				CPUMilli:    12000,
				MemoryBytes: 25769803776,
			},
		},
		Nodes: []safety.NodeAllocatableSummary{
			{
				Name:        "pool-b-1",
				Schedulable: true,
				Summary: safety.AllocatableSummary{
					Allocatable: safety.Resources{CPUMilli: 4000, MemoryBytes: 8589934592},
					Requested:   safety.Resources{CPUMilli: 3600, MemoryBytes: 6442450944},
				},
			},
			{
				Name:        "pool-b-2",
				Schedulable: true,
				Summary: safety.AllocatableSummary{
					Allocatable: safety.Resources{CPUMilli: 4000, MemoryBytes: 8589934592},
					Requested:   safety.Resources{CPUMilli: 3500, MemoryBytes: 8053063680},
				},
			},
			{
				Name:        "pool-b-3",
				Schedulable: true,
				Summary: safety.AllocatableSummary{
					Allocatable: safety.Resources{CPUMilli: 4000, MemoryBytes: 8589934592},
					Requested:   safety.Resources{CPUMilli: 3000, MemoryBytes: 7516192768},
				},
			},
		},
	}

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.State != StateSuppressed {
		t.Fatalf("expected suppressed state, got %q", result.State)
	}
	assertReasonCode(t, result.SuppressionReasons, explain.ReasonInsufficientNodeHeadroom)
	if result.Explanation.SafetyChecks.NodeHeadroomPassed == nil || *result.Explanation.SafetyChecks.NodeHeadroomPassed {
		t.Fatal("expected explanation to record failed node headroom check")
	}
	if result.Explanation.Inputs.NodeHeadroom == nil || result.Explanation.Inputs.NodeHeadroom.Status != string(safety.HeadroomStatusInsufficient) {
		t.Fatalf("expected insufficient node headroom explanation, got %#v", result.Explanation.Inputs.NodeHeadroom)
	}
}

func TestDeterministicEngineReturnsUnavailableWhenForecastDoesNotCoverWarmup(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.ForecastTimestamp = input.EvaluationTime.Add(30 * time.Second)
	input.EstimatedWarmup = 45 * time.Second

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.State != StateUnavailable {
		t.Fatalf("expected unavailable state, got %q", result.State)
	}
	assertReasonCode(t, result.SuppressionReasons, explain.ReasonForecastHorizonTooShort)
	if result.RawRequiredReplicas != 0 {
		t.Fatalf("expected no computed candidate, got raw replicas %d", result.RawRequiredReplicas)
	}
}

func TestDeterministicEngineReturnsUnavailableWhenPositiveForecastHasNoDemandBaseline(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.CurrentDemand = 0
	input.ForecastedDemand = 100

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.State != StateUnavailable {
		t.Fatalf("expected unavailable state, got %q", result.State)
	}
	assertReasonCode(t, result.SuppressionReasons, explain.ReasonNoCurrentDemandBaseline)
}

func TestDeterministicEngineHandlesZeroDemandByDrivingTowardMinReplicas(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.CurrentDemand = 0
	input.CurrentReplicas = 6
	input.ForecastedDemand = 0
	input.MinReplicas = 2
	input.MaxStepDown = int32Ptr(2)

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.State != StateAvailable {
		t.Fatalf("expected available state, got %q", result.State)
	}
	if result.RawRequiredReplicas != 0 {
		t.Fatalf("expected raw replicas 0, got %d", result.RawRequiredReplicas)
	}
	if result.FinalRecommendedReplicas != 4 {
		t.Fatalf("expected step-down toward min replicas to 4, got %d", result.FinalRecommendedReplicas)
	}
}

func TestDeterministicEngineCorrectsCurrentReplicasBelowMinBeforeStepBounds(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.CurrentDemand = 0
	input.CurrentReplicas = 0
	input.ForecastedDemand = 0
	input.MinReplicas = 2
	input.MaxStepUp = int32Ptr(1)

	result, err := engine.Recommend(input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.State != StateAvailable {
		t.Fatalf("expected available state, got %q", result.State)
	}
	if result.FinalRecommendedReplicas != 2 {
		t.Fatalf("expected hard min correction to 2 replicas, got %d", result.FinalRecommendedReplicas)
	}
}

func TestDeterministicEngineRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	engine := DeterministicEngine{}
	input := validInput()
	input.TargetUtilization = 0

	if _, err := engine.Recommend(input); err == nil {
		t.Fatal("expected invalid input error")
	}
}

func validInput() Input {
	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	return Input{
		Workload:            "default/checkout-api",
		EvaluationTime:      now,
		ForecastMethod:      "seasonalNaive",
		ForecastedDemand:    320,
		ForecastTimestamp:   now.Add(45 * time.Second),
		CurrentDemand:       200,
		CurrentReplicas:     4,
		TargetUtilization:   0.8,
		EstimatedWarmup:     45 * time.Second,
		ConfidenceScore:     0.9,
		ConfidenceThreshold: 0.7,
		MinReplicas:         2,
		MaxReplicas:         10,
		CooldownWindow:      5 * time.Minute,
		NodeHeadroomMode:    safety.NodeHeadroomModeDisabled,
	}
}

func int32Ptr(value int32) *int32 {
	return &value
}

func assertReasonCode(t *testing.T, reasons []explain.SuppressionReason, code string) {
	t.Helper()
	for _, reason := range reasons {
		if reason.Code == code {
			return
		}
	}
	t.Fatalf("expected reason code %q, got %#v", code, reasons)
}

func assertBoundDetailCode(t *testing.T, details []explain.BoundDetail, code string) {
	t.Helper()
	for _, detail := range details {
		if detail.Code == code {
			return
		}
	}
	t.Fatalf("expected bound detail code %q, got %#v", code, details)
}
