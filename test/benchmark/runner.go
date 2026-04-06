package benchmark

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/oswalpalash/skale/internal/explain"
	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/recommend"
	"github.com/oswalpalash/skale/internal/replay"
	"github.com/oswalpalash/skale/internal/safety"
)

// Runner executes deterministic replay benchmarks over synthetic scenarios.
//
// The runner deliberately reuses the production replay path. It only adds one narrow hook for
// benchmark-only node headroom timelines because v1 replay otherwise has no structured historical
// headroom fixture interface.
type Runner struct {
	ForecastModel      forecast.Model
	ReadinessEvaluator metrics.Evaluator
	SafetyEvaluator    safety.Evaluator
	ExplainBuilder     explain.Builder
}

// ScenarioResult captures the replay output and scorecard for one synthetic scenario.
type ScenarioResult struct {
	Scenario Scenario
	Replay   replay.Result
	Score    Scorecard
}

// SuiteSummary is the aggregate replay benchmark digest for a scenario suite.
type SuiteSummary struct {
	ScenarioCount              int            `json:"scenarioCount,omitempty"`
	CompleteCount              int            `json:"completeCount,omitempty"`
	UnsupportedCount           int            `json:"unsupportedCount,omitempty"`
	TotalRecommendationEvents  int            `json:"totalRecommendationEvents,omitempty"`
	TotalSuppressedEvaluations int            `json:"totalSuppressedEvaluations,omitempty"`
	TotalOverloadMinutesDelta  float64        `json:"totalOverloadMinutesDelta,omitempty"`
	TotalExcessHeadroomDelta   float64        `json:"totalExcessHeadroomDelta,omitempty"`
	SuppressionReasonTotals    map[string]int `json:"suppressionReasonTotals,omitempty"`
}

// SuiteResult is the benchmark harness output for a set of scenarios.
type SuiteResult struct {
	Summary SuiteSummary     `json:"summary"`
	Results []ScenarioResult `json:"results,omitempty"`
}

// Scorecard summarizes forecast and recommendation quality proxies for one replay result.
type Scorecard struct {
	Forecast       ForecastScore       `json:"forecast"`
	Recommendation RecommendationScore `json:"recommendation"`
}

// ForecastScore summarizes selected forecast accuracy and confidence over replay evaluations.
type ForecastScore struct {
	SampleCount            int     `json:"sampleCount,omitempty"`
	MeanAbsoluteError      float64 `json:"meanAbsoluteError,omitempty"`
	MeanNormalizedError    float64 `json:"meanNormalizedError,omitempty"`
	RootMeanSquareError    float64 `json:"rootMeanSquareError,omitempty"`
	MeanConfidence         float64 `json:"meanConfidence,omitempty"`
	HighReliabilityCount   int     `json:"highReliabilityCount,omitempty"`
	MediumReliabilityCount int     `json:"mediumReliabilityCount,omitempty"`
	LowReliabilityCount    int     `json:"lowReliabilityCount,omitempty"`
	UnsupportedCount       int     `json:"unsupportedCount,omitempty"`
}

// RecommendationScore summarizes replay recommendation and suppression behavior.
type RecommendationScore struct {
	Status                  replay.Status  `json:"status,omitempty"`
	RecommendationEvents    int            `json:"recommendationEvents,omitempty"`
	AvailableEvaluations    int            `json:"availableEvaluations,omitempty"`
	SuppressedEvaluations   int            `json:"suppressedEvaluations,omitempty"`
	UnavailableEvaluations  int            `json:"unavailableEvaluations,omitempty"`
	OverloadMinutesBaseline float64        `json:"overloadMinutesBaseline,omitempty"`
	OverloadMinutesReplay   float64        `json:"overloadMinutesReplay,omitempty"`
	OverloadMinutesDelta    float64        `json:"overloadMinutesDelta,omitempty"`
	ExcessHeadroomBaseline  float64        `json:"excessHeadroomBaseline,omitempty"`
	ExcessHeadroomReplay    float64        `json:"excessHeadroomReplay,omitempty"`
	ExcessHeadroomDelta     float64        `json:"excessHeadroomDelta,omitempty"`
	SuppressionReasonCounts map[string]int `json:"suppressionReasonCounts,omitempty"`
	UnsupportedReasons      []string       `json:"unsupportedReasons,omitempty"`
}

// RunScenario executes replay for one scenario and scores the resulting replay output.
func (r Runner) RunScenario(ctx context.Context, scenario Scenario) (ScenarioResult, error) {
	engine := replay.Engine{
		Metrics:   staticProvider{snapshot: scenario.Fixture.Snapshot},
		Forecast:  r.ForecastModel,
		Safety:    r.SafetyEvaluator,
		Explain:   r.ExplainBuilder,
		Readiness: r.ReadinessEvaluator,
	}
	if len(scenario.Fixture.NodeHeadroom) > 0 {
		engine.Recommend = headroomInjectingEngine{
			Safety:       r.SafetyEvaluator,
			Explain:      r.ExplainBuilder,
			Observations: scenario.Fixture.NodeHeadroom,
		}
	}
	result, err := engine.Run(ctx, scenario.Fixture.Spec)
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("run replay for scenario %q: %w", scenario.Name, err)
	}
	return ScenarioResult{
		Scenario: scenario,
		Replay:   result,
		Score:    ScoreScenario(result, scenario.Fixture.Snapshot),
	}, nil
}

// RunSuite executes the provided scenarios in order and aggregates their scorecards.
func (r Runner) RunSuite(ctx context.Context, scenarios []Scenario) (SuiteResult, error) {
	out := SuiteResult{
		Summary: SuiteSummary{
			SuppressionReasonTotals: map[string]int{},
		},
		Results: make([]ScenarioResult, 0, len(scenarios)),
	}
	for _, scenario := range scenarios {
		result, err := r.RunScenario(ctx, scenario)
		if err != nil {
			return SuiteResult{}, err
		}
		out.Results = append(out.Results, result)
		out.Summary.ScenarioCount++
		if result.Replay.Status == replay.StatusUnsupported {
			out.Summary.UnsupportedCount++
		} else {
			out.Summary.CompleteCount++
		}
		out.Summary.TotalRecommendationEvents += result.Score.Recommendation.RecommendationEvents
		out.Summary.TotalSuppressedEvaluations += result.Score.Recommendation.SuppressedEvaluations
		out.Summary.TotalOverloadMinutesDelta += result.Score.Recommendation.OverloadMinutesDelta
		out.Summary.TotalExcessHeadroomDelta += result.Score.Recommendation.ExcessHeadroomDelta
		for code, count := range result.Score.Recommendation.SuppressionReasonCounts {
			out.Summary.SuppressionReasonTotals[code] += count
		}
	}
	return out, nil
}

// ScoreScenario computes forecast and recommendation scorecards from replay output and actual demand.
func ScoreScenario(result replay.Result, snapshot metrics.Snapshot) Scorecard {
	return Scorecard{
		Forecast:       ScoreForecast(result, snapshot),
		Recommendation: ScoreRecommendation(result),
	}
}

// ScoreForecast compares selected replay forecast points to the actual historical demand at the predicted timestamps.
func ScoreForecast(result replay.Result, snapshot metrics.Snapshot) ForecastScore {
	score := ForecastScore{}
	var absoluteSum float64
	var squaredSum float64
	var normalizedSum float64
	var confidenceSum float64

	for _, evaluation := range result.Evaluations {
		if evaluation.Forecast.ForecastFor.IsZero() {
			score.UnsupportedCount++
			continue
		}
		actual, ok := seriesValueAt(snapshot.Demand.Samples, evaluation.Forecast.ForecastFor)
		if !ok {
			score.UnsupportedCount++
			continue
		}
		errValue := math.Abs(actual - evaluation.Forecast.PredictedDemand)
		absoluteSum += errValue
		squaredSum += errValue * errValue
		normalizedSum += errValue / math.Max(actual, 1)
		confidenceSum += evaluation.Forecast.Confidence
		score.SampleCount++

		switch strings.ToLower(strings.TrimSpace(evaluation.Forecast.Reliability)) {
		case string(forecast.ReliabilityHigh):
			score.HighReliabilityCount++
		case string(forecast.ReliabilityMedium):
			score.MediumReliabilityCount++
		case string(forecast.ReliabilityLow):
			score.LowReliabilityCount++
		default:
			score.UnsupportedCount++
		}
	}
	if score.SampleCount == 0 {
		return score
	}

	score.MeanAbsoluteError = absoluteSum / float64(score.SampleCount)
	score.MeanNormalizedError = normalizedSum / float64(score.SampleCount)
	score.RootMeanSquareError = math.Sqrt(squaredSum / float64(score.SampleCount))
	score.MeanConfidence = confidenceSum / float64(score.SampleCount)
	return score
}

// ScoreRecommendation summarizes replay outcome and suppression behavior deltas relative to baseline.
func ScoreRecommendation(result replay.Result) RecommendationScore {
	return RecommendationScore{
		Status:                  result.Status,
		RecommendationEvents:    result.Replay.RecommendationEventCount,
		AvailableEvaluations:    result.Replay.AvailableCount,
		SuppressedEvaluations:   result.Replay.SuppressedCount,
		UnavailableEvaluations:  result.Replay.UnavailableCount,
		OverloadMinutesBaseline: result.Baseline.OverloadMinutesProxy,
		OverloadMinutesReplay:   result.Replay.OverloadMinutesProxy,
		OverloadMinutesDelta:    result.Replay.OverloadMinutesProxy - result.Baseline.OverloadMinutesProxy,
		ExcessHeadroomBaseline:  result.Baseline.ExcessHeadroomMinutesProxy,
		ExcessHeadroomReplay:    result.Replay.ExcessHeadroomMinutesProxy,
		ExcessHeadroomDelta:     result.Replay.ExcessHeadroomMinutesProxy - result.Baseline.ExcessHeadroomMinutesProxy,
		SuppressionReasonCounts: cloneCounts(result.Replay.SuppressionReasonCounts),
		UnsupportedReasons:      append([]string(nil), result.UnsupportedReasons...),
	}
}

// SortedScenarioNames returns scenario names in a stable lexical order for diagnostics.
func SortedScenarioNames(scenarios []Scenario) []string {
	names := make([]string, 0, len(scenarios))
	for _, scenario := range scenarios {
		names = append(names, scenario.Name)
	}
	sort.Strings(names)
	return names
}

type staticProvider struct {
	snapshot metrics.Snapshot
}

func (p staticProvider) LoadWindow(context.Context, metrics.Target, metrics.Window) (metrics.Snapshot, error) {
	return p.snapshot, nil
}

type headroomInjectingEngine struct {
	Delegate     recommend.Engine
	Safety       safety.Evaluator
	Explain      explain.Builder
	Observations []HeadroomObservation
}

func (e headroomInjectingEngine) Recommend(input recommend.Input) (recommend.Result, error) {
	if input.NodeHeadroom == nil {
		input.NodeHeadroom = headroomAt(e.Observations, input.EvaluationTime)
	}
	delegate := e.Delegate
	if delegate == nil {
		delegate = recommend.DeterministicEngine{
			Safety:  e.Safety,
			Explain: e.Explain,
		}
	}
	return delegate.Recommend(input)
}

func headroomAt(observations []HeadroomObservation, at time.Time) *safety.NodeHeadroomSignal {
	var selected *safety.NodeHeadroomSignal
	for _, observation := range observations {
		if observation.ObservedAt.After(at) {
			continue
		}
		candidate := observation.Signal
		selected = &candidate
	}
	if selected == nil && len(observations) > 0 {
		candidate := observations[0].Signal
		selected = &candidate
	}
	return selected
}

func seriesValueAt(samples []metrics.Sample, at time.Time) (float64, bool) {
	var (
		value float64
		ok    bool
	)
	for _, sample := range samples {
		if sample.Timestamp.After(at) {
			break
		}
		value = sample.Value
		ok = true
	}
	return value, ok
}

func cloneCounts(values map[string]int) map[string]int {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]int, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
