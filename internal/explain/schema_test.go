package explain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/metrics"
)

func TestDefaultBuilderPopulatesSharedExplainabilitySchema(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	telemetry := TelemetryReadinessSummary{
		CheckedAt: now,
		State:     "degraded",
		Message:   "telemetry readiness is degraded",
		Reasons:   []string{"cpu signal has large gaps"},
		Signals: []TelemetrySignalSummary{{
			Name:     "cpu",
			State:    "degraded",
			Required: true,
			Message:  "cpu signal has large gaps",
		}},
	}
	forecastSummary := ForecastSummary{
		EvaluatedAt:     now,
		GeneratedAt:     now,
		Method:          "seasonal_naive",
		HorizonSeconds:  300,
		ForecastFor:     now.Add(90 * time.Second),
		PredictedDemand: 320,
		Confidence:      0.82,
		Message:         "seasonal_naive predicted 320.00 demand for 2026-04-02T12:01:30Z at confidence 0.82",
	}

	decision := DefaultBuilder{}.Build(BuildInput{
		WorkloadRef:                 WorkloadIdentity{Namespace: "payments", Name: "checkout-api", Kind: "Deployment"},
		EvaluationTime:              now,
		RecommendationTime:          now,
		TargetReadyTime:             now.Add(90 * time.Second),
		ForecastMethod:              forecastSummary.Method,
		ForecastedDemand:            forecastSummary.PredictedDemand,
		ForecastTimestamp:           forecastSummary.ForecastFor,
		ForecastSummary:             &forecastSummary,
		CurrentDemand:               160,
		CurrentReplicas:             2,
		TargetUtilization:           0.8,
		Warmup:                      90 * time.Second,
		Telemetry:                   &telemetry,
		ConfidenceScore:             0.82,
		ConfidenceThreshold:         0.9,
		MinReplicas:                 2,
		MaxReplicas:                 10,
		EffectivePerReplicaCapacity: 100,
		RawRequiredReplicas:         4,
		PolicyBoundReplicas:         4,
		StepBoundReplicas:           4,
		FinalRecommendedReplicas:    4,
		Delta:                       2,
		State:                       "suppressed",
		Suppressed:                  true,
		SuppressionReasons: []SuppressionReason{{
			Code:     ReasonLowConfidence,
			Category: SuppressionCategoryForecast,
			Severity: SuppressionSeverityWarning,
			Message:  "confidence score 0.82 is below threshold 0.90",
		}},
	})

	if decision.Workload.Namespace != "payments" || decision.Workload.Name != "checkout-api" {
		t.Fatalf("unexpected workload identity %#v", decision.Workload)
	}
	if decision.Signals.CurrentReplicas != 2 || decision.Signals.CurrentDemand != 160 {
		t.Fatalf("unexpected signal summary %#v", decision.Signals)
	}
	if decision.Forecast.Method != "seasonal_naive" || decision.Forecast.PredictedDemand != 320 {
		t.Fatalf("unexpected forecast summary %#v", decision.Forecast)
	}
	if decision.Telemetry == nil || decision.Telemetry.State != "degraded" {
		t.Fatalf("unexpected telemetry summary %#v", decision.Telemetry)
	}
	if decision.Suppression == nil {
		t.Fatal("expected suppression explanation")
	}
	if len(decision.Suppression.Reasons) != 1 || decision.Suppression.Reasons[0].Code != ReasonLowConfidence {
		t.Fatalf("unexpected suppression reasons %#v", decision.Suppression.Reasons)
	}
}

func TestTelemetrySummaryAndStatusProjection(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	report := metrics.ReadinessReport{
		Level:           metrics.ReadinessLevelUnsupported,
		CheckedAt:       now,
		Summary:         "telemetry is unsupported",
		Reasons:         []string{"demand signal is missing 40% of expected samples"},
		BlockingReasons: []string{"warmup signal is missing"},
		Signals: []metrics.SignalReport{{
			Name:     metrics.SignalDemand,
			Required: true,
			Level:    metrics.SignalLevelUnsupported,
			Message:  "demand signal is missing 40% of expected samples",
		}},
	}

	summary := TelemetrySummaryFromReadiness(report)
	if summary.State != "unsupported" {
		t.Fatalf("state = %q, want unsupported", summary.State)
	}

	projector := StatusProjection{}
	status := projector.Telemetry(summary)
	if status == nil {
		t.Fatal("expected telemetry status projection")
	}
	if status.State != "unsupported" {
		t.Fatalf("status state = %q, want unsupported", status.State)
	}
	if len(status.Signals) != 1 || status.Signals[0].Name != "demand" {
		t.Fatalf("unexpected status signals %#v", status.Signals)
	}

	forecastStatus := projector.Forecast(ForecastSummary{
		EvaluatedAt:     now,
		Method:          "seasonal_naive",
		HorizonSeconds:  300,
		PredictedDemand: 320,
		Confidence:      0.88,
		Message:         "seasonal_naive predicted 320.00 demand",
	})
	if forecastStatus == nil || forecastStatus.Method != "seasonal_naive" {
		t.Fatalf("unexpected forecast status %#v", forecastStatus)
	}

	recommendationStatus := projector.Recommendation(DefaultBuilder{}.Build(BuildInput{
		WorkloadRef:                 WorkloadIdentity{Namespace: "payments", Name: "checkout-api"},
		EvaluationTime:              now,
		ForecastMethod:              "seasonal_naive",
		ForecastedDemand:            320,
		ForecastTimestamp:           now.Add(time.Minute),
		CurrentDemand:               160,
		CurrentReplicas:             2,
		TargetUtilization:           0.8,
		Warmup:                      time.Minute,
		ConfidenceScore:             0.88,
		ConfidenceThreshold:         0.7,
		MinReplicas:                 2,
		MaxReplicas:                 10,
		EffectivePerReplicaCapacity: 100,
		RawRequiredReplicas:         4,
		PolicyBoundReplicas:         4,
		StepBoundReplicas:           4,
		FinalRecommendedReplicas:    4,
		Delta:                       2,
		State:                       "available",
	}))
	if recommendationStatus == nil || recommendationStatus.RecommendedReplicas != 4 {
		t.Fatalf("unexpected recommendation status %#v", recommendationStatus)
	}
}

func TestReplayEventExplanationSerializesStableJSON(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	result := forecast.Result{
		Model:       "seasonal_naive",
		GeneratedAt: now,
		Horizon:     5 * time.Minute,
		Confidence:  0.91,
		Reliability: forecast.ReliabilityHigh,
		Validation: forecast.Validation{
			NormalizedError: 0.08,
		},
		Advisories: []forecast.Advisory{{
			Code:    forecast.AdvisoryLimitedHistory,
			Message: "history only covered one prior season",
		}},
	}
	forecastSummary := ForecastSummaryFromResult(result, forecast.Point{
		Timestamp: now.Add(2 * time.Minute),
		Value:     320,
	}, now)

	event := ReplayEventExplanation{
		Workload:         WorkloadIdentity{Namespace: "payments", Name: "checkout-api"},
		EvaluatedAt:      now,
		ActivationTime:   timePtr(now.Add(2 * time.Minute)),
		BaselineReplicas: 2,
		ReplayReplicas:   2,
		Signals: SignalSummary{
			ObservedAt:            now,
			CurrentDemand:         160,
			CurrentReplicas:       2,
			RequiredReplicasProxy: int32Ptr(4),
		},
		Forecast: forecastSummary,
		Recommendation: RecommendationSurface{
			State:               "available",
			CurrentReplicas:     2,
			RecommendedReplicas: 4,
			Delta:               2,
			Message:             "seasonal_naive forecast 320.00 for readiness at 2026-04-02T12:02:00Z implied 4 raw replicas; final recommendation 4 replicas.",
		},
		Summary: "seasonal_naive forecast 320.00 for readiness at 2026-04-02T12:02:00Z implied 4 raw replicas; final recommendation 4 replicas.",
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	text := string(encoded)
	for _, expected := range []string{
		`"workload":{"namespace":"payments","name":"checkout-api"}`,
		`"forecast":{"evaluatedAt":"2026-04-02T12:00:00Z","generatedAt":"2026-04-02T12:00:00Z","method":"seasonal_naive"`,
		`"recommendation":{"state":"available","currentReplicas":2,"recommendedReplicas":4,"delta":2`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in JSON: %s", expected, text)
		}
	}
}

func TestBuildSuppressionExplanationSynthesizesFallbackMessage(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	reasons := []SuppressionReason{
		{
			Code:     ReasonLowConfidence,
			Category: SuppressionCategoryForecast,
			Severity: SuppressionSeverityWarning,
			Message:  "confidence 0.40 is below threshold 0.70",
		},
		{
			Code:     ReasonTelemetryNotReady,
			Category: SuppressionCategoryTelemetry,
			Severity: SuppressionSeverityError,
			Message:  "telemetry readiness is degraded",
		},
	}

	explanation := BuildSuppressionExplanation(
		WorkloadIdentity{Namespace: "payments", Name: "checkout-api"},
		now,
		SignalSummary{ObservedAt: now, CurrentDemand: 160, CurrentReplicas: 2},
		ForecastSummary{Method: "seasonal_naive", ForecastFor: now.Add(time.Minute), PredictedDemand: 320},
		BoundsApplied{},
		"suppressed",
		reasons,
		"",
	)
	if explanation == nil {
		t.Fatal("expected suppression explanation")
	}
	if explanation.Message != "recommendation suppressed: low_confidence, telemetry_not_ready" {
		t.Fatalf("message = %q", explanation.Message)
	}

	reasons[0].Code = "mutated"
	if explanation.Reasons[0].Code != ReasonLowConfidence {
		t.Fatalf("expected explanation reasons to be copied, got %#v", explanation.Reasons)
	}
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func int32Ptr(value int32) *int32 {
	return &value
}
