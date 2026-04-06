package safety

import (
	"testing"
	"time"

	"github.com/oswalpalash/skale/internal/explain"
)

func TestDefaultEvaluatorAppliesBoundsWithoutSuppressing(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	result, err := evaluator.Evaluate(validInput(func(input *Input) {
		input.CurrentReplicas = 4
		input.RawProposedReplicas = 12
		input.MinReplicas = 2
		input.MaxReplicas = 10
		input.MaxStepUp = int32Ptr(3)
	}))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if result.Suppressed {
		t.Fatalf("expected unsuppressed result, got %#v", result.Reasons)
	}
	if result.PolicyBoundReplicas != 10 {
		t.Fatalf("policy bound replicas = %d, want 10", result.PolicyBoundReplicas)
	}
	if result.FinalProposedReplicas != 7 {
		t.Fatalf("final proposed replicas = %d, want 7", result.FinalProposedReplicas)
	}
	if !result.MinMaxBounded || !result.StepUpBounded {
		t.Fatalf("expected min/max and step-up bounds, got %#v", result)
	}
	if len(result.BoundDetails) != 2 {
		t.Fatalf("bound details = %#v, want 2 entries", result.BoundDetails)
	}
}

func TestDefaultEvaluatorSuppressesLowConfidence(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	result, err := evaluator.Evaluate(validInput(func(input *Input) {
		input.ConfidenceScore = 0.6
		input.ConfidenceThreshold = 0.7
	}))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if !result.Suppressed {
		t.Fatal("expected low confidence suppression")
	}
	if result.Checks.ConfidencePassed {
		t.Fatal("expected confidence check to fail")
	}
	assertReason(t, result.Reasons, explain.ReasonLowConfidence, explain.SuppressionCategoryForecast, explain.SuppressionSeverityWarning)
}

func TestDefaultEvaluatorSuppressesTelemetryNotReady(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	result, err := evaluator.Evaluate(validInput(func(input *Input) {
		input.Telemetry = &TelemetryStatus{
			Level:   TelemetryLevelDegraded,
			Reasons: []string{"demand signal has 35% missing samples"},
		}
	}))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if !result.Suppressed {
		t.Fatal("expected telemetry suppression")
	}
	if result.Checks.TelemetryReady == nil || *result.Checks.TelemetryReady {
		t.Fatalf("expected telemetry readiness failure, got %#v", result.Checks.TelemetryReady)
	}
	assertReason(t, result.Reasons, explain.ReasonTelemetryNotReady, explain.SuppressionCategoryTelemetry, explain.SuppressionSeverityError)
}

func TestDefaultEvaluatorSuppressesModelDivergence(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	result, err := evaluator.Evaluate(validInput(func(input *Input) {
		input.ModelDivergence = &ModelDivergenceStatus{
			Divergence:     0.41,
			MaximumAllowed: 0.25,
		}
	}))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if !result.Suppressed {
		t.Fatal("expected model divergence suppression")
	}
	if result.Checks.ModelDivergencePassed == nil || *result.Checks.ModelDivergencePassed {
		t.Fatalf("expected model divergence failure, got %#v", result.Checks.ModelDivergencePassed)
	}
	assertReason(t, result.Reasons, explain.ReasonModelDivergenceTooHigh, explain.SuppressionCategoryModel, explain.SuppressionSeverityWarning)
}

func TestDefaultEvaluatorSuppressesRecentForecastError(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	result, err := evaluator.Evaluate(validInput(func(input *Input) {
		input.ForecastError = &ForecastErrorStatus{
			NormalizedError: 0.38,
			MaximumAllowed:  0.20,
		}
	}))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if !result.Suppressed {
		t.Fatal("expected forecast error suppression")
	}
	if result.Checks.RecentForecastErrorPassed == nil || *result.Checks.RecentForecastErrorPassed {
		t.Fatalf("expected forecast error failure, got %#v", result.Checks.RecentForecastErrorPassed)
	}
	assertReason(t, result.Reasons, explain.ReasonForecastErrorTooHigh, explain.SuppressionCategoryModel, explain.SuppressionSeverityWarning)
}

func TestDefaultEvaluatorSuppressesBlackoutWindow(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	now := baseTime()
	result, err := evaluator.Evaluate(validInput(func(input *Input) {
		input.BlackoutWindows = []BlackoutWindow{{
			Name:   "deploy-freeze",
			Start:  now.Add(-time.Minute),
			End:    now.Add(time.Minute),
			Reason: "planned rollout window",
		}}
	}))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if !result.Suppressed {
		t.Fatal("expected blackout suppression")
	}
	if result.Checks.BlackoutPassed {
		t.Fatal("expected blackout check to fail")
	}
	assertReason(t, result.Reasons, explain.ReasonBlackoutWindowActive, explain.SuppressionCategoryPolicy, explain.SuppressionSeverityWarning)
}

func TestDefaultEvaluatorSuppressesDependencyHealthFailure(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	result, err := evaluator.Evaluate(validInput(func(input *Input) {
		input.DependencyHealth = []DependencyHealthStatus{{
			Name:                "payments-db",
			Healthy:             true,
			HealthyRatio:        0.72,
			MinimumHealthyRatio: 0.90,
		}}
	}))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if !result.Suppressed {
		t.Fatal("expected dependency suppression")
	}
	if result.Checks.DependencyHealthPassed == nil || *result.Checks.DependencyHealthPassed {
		t.Fatalf("expected dependency health failure, got %#v", result.Checks.DependencyHealthPassed)
	}
	assertReason(t, result.Reasons, explain.ReasonDependencyHealthFailed, explain.SuppressionCategoryDependency, explain.SuppressionSeverityError)
}

func TestDefaultEvaluatorSuppressesCooldownChange(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	now := baseTime()
	result, err := evaluator.Evaluate(validInput(func(input *Input) {
		input.RawProposedReplicas = 8
		input.CooldownWindow = 5 * time.Minute
		input.LastRecommendation = &PreviousRecommendation{
			RecommendedReplicas: 6,
			ChangedAt:           now.Add(-2 * time.Minute),
		}
	}))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if !result.Suppressed {
		t.Fatal("expected cooldown suppression")
	}
	if result.Checks.CooldownPassed {
		t.Fatal("expected cooldown check to fail")
	}
	assertReason(t, result.Reasons, explain.ReasonCooldownActive, explain.SuppressionCategoryStabilization, explain.SuppressionSeverityWarning)
}

func TestDefaultEvaluatorPassesReadyNodeHeadroom(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	fixture := loadHeadroomFixture(t, "headroom_sufficient.json")
	result, err := evaluator.Evaluate(validInput(func(input *Input) {
		input.RawProposedReplicas = 7
		input.NodeHeadroomMode = NodeHeadroomModeRequireForScaleUp
		input.NodeHeadroom = &fixture.Signal
	}))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if result.Suppressed {
		t.Fatalf("expected headroom to pass, got %#v", result.Reasons)
	}
	if result.Checks.NodeHeadroomPassed == nil || !*result.Checks.NodeHeadroomPassed {
		t.Fatalf("expected node headroom pass, got %#v", result.Checks.NodeHeadroomPassed)
	}
	if result.NodeHeadroom == nil || result.NodeHeadroom.Status != HeadroomStatusSufficient {
		t.Fatalf("expected sufficient headroom assessment, got %#v", result.NodeHeadroom)
	}
}

func TestDefaultEvaluatorSuppressesInsufficientNodeHeadroom(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	fixture := loadHeadroomFixture(t, "headroom_insufficient_node_packing.json")
	result, err := evaluator.Evaluate(validInput(func(input *Input) {
		input.RawProposedReplicas = 8
		input.NodeHeadroomMode = NodeHeadroomModeRequireForScaleUp
		input.NodeHeadroom = &fixture.Signal
	}))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if !result.Suppressed {
		t.Fatal("expected node headroom suppression")
	}
	if result.Checks.NodeHeadroomPassed == nil || *result.Checks.NodeHeadroomPassed {
		t.Fatalf("expected node headroom failure, got %#v", result.Checks.NodeHeadroomPassed)
	}
	assertReason(t, result.Reasons, explain.ReasonInsufficientNodeHeadroom, explain.SuppressionCategoryCluster, explain.SuppressionSeverityError)
}

func TestDefaultEvaluatorSuppressesUncertainNodeHeadroom(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	fixture := loadHeadroomFixture(t, "headroom_uncertain_aggregate_only.json")
	result, err := evaluator.Evaluate(validInput(func(input *Input) {
		input.RawProposedReplicas = 8
		input.NodeHeadroomMode = NodeHeadroomModeRequireForScaleUp
		input.NodeHeadroom = &fixture.Signal
	}))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if !result.Suppressed {
		t.Fatal("expected uncertain node headroom suppression")
	}
	if result.Checks.NodeHeadroomStatus == nil || *result.Checks.NodeHeadroomStatus != HeadroomStatusUncertain {
		t.Fatalf("expected uncertain headroom status, got %#v", result.Checks.NodeHeadroomStatus)
	}
	assertReason(t, result.Reasons, explain.ReasonUncertainNodeHeadroom, explain.SuppressionCategoryCluster, explain.SuppressionSeverityWarning)
}

func TestDefaultEvaluatorSuppressesOperatorModes(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	tests := []struct {
		name     string
		mode     OperatorMode
		code     string
		severity explain.SuppressionSeverity
	}{
		{
			name:     "paused",
			mode:     OperatorModePaused,
			code:     explain.ReasonOperatorPaused,
			severity: explain.SuppressionSeverityWarning,
		},
		{
			name:     "disabled",
			mode:     OperatorModeDisabled,
			code:     explain.ReasonOperatorDisabled,
			severity: explain.SuppressionSeverityError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := evaluator.Evaluate(validInput(func(input *Input) {
				input.OperatorMode = tt.mode
			}))
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}

			if !result.Suppressed {
				t.Fatal("expected operator mode suppression")
			}
			if result.Checks.OperatorEnabled {
				t.Fatal("expected operator enabled flag to be false")
			}
			assertReason(t, result.Reasons, tt.code, explain.SuppressionCategoryOperator, tt.severity)
		})
	}
}

func TestDefaultEvaluatorSuppressesCircuitBreaker(t *testing.T) {
	t.Parallel()

	evaluator := DefaultEvaluator{}
	result, err := evaluator.Evaluate(validInput(func(input *Input) {
		input.CircuitBreaker = &CircuitBreaker{
			ConsecutivePoorEvaluations:    3,
			MaxConsecutivePoorEvaluations: 3,
		}
	}))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if !result.Suppressed {
		t.Fatal("expected circuit breaker suppression")
	}
	if result.Checks.CircuitBreakerClosed == nil || *result.Checks.CircuitBreakerClosed {
		t.Fatalf("expected circuit breaker to be open, got %#v", result.Checks.CircuitBreakerClosed)
	}
	assertReason(t, result.Reasons, explain.ReasonCircuitBreakerOpen, explain.SuppressionCategoryPolicy, explain.SuppressionSeverityError)
}

func validInput(mutators ...func(*Input)) Input {
	input := Input{
		EvaluationTime:      baseTime(),
		CurrentReplicas:     4,
		RawProposedReplicas: 6,
		MinReplicas:         2,
		MaxReplicas:         10,
		ConfidenceScore:     0.9,
		ConfidenceThreshold: 0.7,
		NodeHeadroomMode:    NodeHeadroomModeDisabled,
	}
	for _, mutate := range mutators {
		mutate(&input)
	}
	return input
}

func baseTime() time.Time {
	return time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
}

func int32Ptr(value int32) *int32 {
	return &value
}

func assertReason(t *testing.T, reasons []explain.SuppressionReason, code string, category explain.SuppressionCategory, severity explain.SuppressionSeverity) {
	t.Helper()

	for _, reason := range reasons {
		if reason.Code == code {
			if reason.Category != category {
				t.Fatalf("reason %q category = %q, want %q", code, reason.Category, category)
			}
			if reason.Severity != severity {
				t.Fatalf("reason %q severity = %q, want %q", code, reason.Severity, severity)
			}
			return
		}
	}

	t.Fatalf("expected reason %q in %#v", code, reasons)
}
