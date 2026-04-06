package benchmark

import (
	"context"
	"testing"

	"github.com/oswalpalash/skale/internal/explain"
	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/replay"
)

func TestDefaultSuiteContainsRequiredScenarios(t *testing.T) {
	t.Parallel()

	scenarios := DefaultSuite()
	got := SortedScenarioNames(scenarios)
	want := []string{
		ScenarioAnomalyOneOffSpike,
		ScenarioDiurnalBurst,
		ScenarioInsufficientTelemetry,
		ScenarioHeadroomConstrained,
		ScenarioNoisyRecurringBurst,
		ScenarioScheduledBurst,
	}
	if len(got) != len(want) {
		t.Fatalf("scenario count = %d, want %d (%v)", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("scenario[%d] = %q, want %q (%v)", index, got[index], want[index], got)
		}
	}
}

func TestRunnerExecutesDefaultSuite(t *testing.T) {
	runner := Runner{
		ForecastModel:  forecast.AutoModel{},
		ExplainBuilder: explain.DefaultBuilder{},
	}
	suite, err := runner.RunSuite(context.Background(), DefaultSuite())
	if err != nil {
		t.Fatalf("RunSuite() error = %v", err)
	}

	if suite.Summary.ScenarioCount != 6 {
		t.Fatalf("scenario count = %d, want 6", suite.Summary.ScenarioCount)
	}
	if suite.Summary.UnsupportedCount != 1 {
		t.Fatalf("unsupported count = %d, want 1", suite.Summary.UnsupportedCount)
	}

	results := map[string]ScenarioResult{}
	for _, result := range suite.Results {
		results[result.Scenario.Name] = result
	}

	diurnal := results[ScenarioDiurnalBurst]
	if diurnal.Replay.Status != replay.StatusComplete {
		t.Fatalf("diurnal status = %q, want %q", diurnal.Replay.Status, replay.StatusComplete)
	}
	if diurnal.Score.Recommendation.RecommendationEvents == 0 {
		t.Fatal("expected diurnal scenario to surface recommendation events")
	}
	if diurnal.Score.Recommendation.OverloadMinutesDelta >= 0 {
		t.Fatalf("expected diurnal overload delta to improve, got %.2f", diurnal.Score.Recommendation.OverloadMinutesDelta)
	}

	scheduled := results[ScenarioScheduledBurst]
	if scheduled.Score.Forecast.SampleCount == 0 {
		t.Fatal("expected scheduled burst scenario to score forecasts")
	}
	if scheduled.Score.Recommendation.OverloadMinutesDelta >= 0 {
		t.Fatalf("expected scheduled burst overload delta to improve, got %.2f", scheduled.Score.Recommendation.OverloadMinutesDelta)
	}

	noisy := results[ScenarioNoisyRecurringBurst]
	if noisy.Score.Forecast.MeanNormalizedError <= 0 {
		t.Fatalf("expected noisy recurring burst forecast error to be positive, got %.4f", noisy.Score.Forecast.MeanNormalizedError)
	}
	if noisy.Score.Recommendation.RecommendationEvents == 0 {
		t.Fatal("expected noisy recurring burst to still surface recommendation events")
	}

	anomaly := results[ScenarioAnomalyOneOffSpike]
	if anomaly.Score.Recommendation.OverloadMinutesDelta < -0.1 {
		t.Fatalf("expected anomaly scenario not to show material overload improvement, got %.2f", anomaly.Score.Recommendation.OverloadMinutesDelta)
	}

	insufficient := results[ScenarioInsufficientTelemetry]
	if insufficient.Replay.Status != replay.StatusUnsupported {
		t.Fatalf("insufficient telemetry status = %q, want %q", insufficient.Replay.Status, replay.StatusUnsupported)
	}
	if len(insufficient.Replay.UnsupportedReasons) == 0 {
		t.Fatal("expected unsupported reasons for insufficient telemetry scenario")
	}

	headroom := results[ScenarioHeadroomConstrained]
	if headroom.Score.Recommendation.RecommendationEvents != 0 {
		t.Fatalf("expected node-headroom-constrained scenario to surface no recommendation events, got %d", headroom.Score.Recommendation.RecommendationEvents)
	}
	if headroom.Score.Recommendation.SuppressionReasonCounts[explain.ReasonInsufficientNodeHeadroom] == 0 {
		t.Fatalf("expected insufficient node headroom suppression, got %#v", headroom.Score.Recommendation.SuppressionReasonCounts)
	}
}

func BenchmarkRunDefaultSuite(b *testing.B) {
	runner := Runner{
		ForecastModel:  forecast.AutoModel{},
		ExplainBuilder: explain.DefaultBuilder{},
	}
	scenarios := DefaultSuite()
	ctx := context.Background()

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := runner.RunSuite(ctx, scenarios); err != nil {
			b.Fatalf("RunSuite() error = %v", err)
		}
	}
}
