package forecast

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestSeasonalNaiveForecastRepeatsLastSeason(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	step := time.Minute
	pattern := []float64{100, 120, 110, 130}
	series := buildSeasonalSeries(start, step, 6, pattern)

	result, err := SeasonalNaiveModel{}.Forecast(context.Background(), Input{
		Series:      series,
		EvaluatedAt: series[len(series)-1].Timestamp,
		Horizon:     4 * time.Minute,
		Step:        step,
		Seasonality: 4 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Forecast() error = %v", err)
	}

	if result.Model != SeasonalNaiveModelName {
		t.Fatalf("model = %q, want %q", result.Model, SeasonalNaiveModelName)
	}
	assertPointValues(t, result.Points, pattern, 1e-9)
	if result.Confidence < 0.99 {
		t.Fatalf("confidence = %.3f, want >= 0.99", result.Confidence)
	}
	if result.Reliability != ReliabilityHigh {
		t.Fatalf("reliability = %q, want %q", result.Reliability, ReliabilityHigh)
	}
}

func TestHoltWintersBeatsSeasonalNaiveOnTrendingSeasonalSeries(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	step := 5 * time.Minute
	seasonality := 20 * time.Minute
	pattern := []float64{0, 18, -8, 10}
	series := buildTrendingSeasonalSeries(start, step, 28, pattern, 2.5)
	inputSeries := series[:24]
	expectedFuture := series[24:28]

	seasonal, err := SeasonalNaiveModel{}.Forecast(context.Background(), Input{
		Series:      inputSeries,
		EvaluatedAt: inputSeries[len(inputSeries)-1].Timestamp,
		Horizon:     seasonality,
		Step:        step,
		Seasonality: seasonality,
	})
	if err != nil {
		t.Fatalf("seasonal naive forecast error = %v", err)
	}

	holtWinters, err := HoltWintersModel{}.Forecast(context.Background(), Input{
		Series:      inputSeries,
		EvaluatedAt: inputSeries[len(inputSeries)-1].Timestamp,
		Horizon:     seasonality,
		Step:        step,
		Seasonality: seasonality,
	})
	if err != nil {
		t.Fatalf("holt-winters forecast error = %v", err)
	}

	if holtWinters.Validation.MeanAbsoluteErr >= seasonal.Validation.MeanAbsoluteErr {
		t.Fatalf(
			"holt-winters MAE = %.3f, seasonal naive MAE = %.3f; want holt-winters lower",
			holtWinters.Validation.MeanAbsoluteErr,
			seasonal.Validation.MeanAbsoluteErr,
		)
	}

	seasonalFutureErr := meanAbsoluteError(pointsToValues(expectedFuture), pointsToValues(seasonal.Points))
	holtFutureErr := meanAbsoluteError(pointsToValues(expectedFuture), pointsToValues(holtWinters.Points))
	if holtFutureErr >= seasonalFutureErr {
		t.Fatalf("holt-winters future MAE = %.3f, seasonal naive future MAE = %.3f; want holt-winters lower", holtFutureErr, seasonalFutureErr)
	}
}

func TestAutoModelPrefersSeasonalNaiveWhenGoodEnough(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	step := time.Minute
	seasonality := 4 * time.Minute
	series := buildSeasonalSeries(start, step, 6, []float64{90, 120, 100, 130})

	result, err := AutoModel{}.Forecast(context.Background(), Input{
		Series:      series,
		EvaluatedAt: series[len(series)-1].Timestamp,
		Horizon:     seasonality,
		Step:        step,
		Seasonality: seasonality,
	})
	if err != nil {
		t.Fatalf("Forecast() error = %v", err)
	}

	if result.Model != SeasonalNaiveModelName {
		t.Fatalf("model = %q, want %q", result.Model, SeasonalNaiveModelName)
	}
	if result.FallbackReason != "" {
		t.Fatalf("fallback reason = %q, want empty", result.FallbackReason)
	}
}

func TestAutoModelFallsBackOnDivergence(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	step := 5 * time.Minute
	seasonality := 20 * time.Minute
	pattern := []float64{0, 10, -6, 8}
	series := buildTrendingSeasonalSeries(start, step, 24, pattern, 4)

	result, err := AutoModel{
		DivergenceThreshold: 0.01,
	}.Forecast(context.Background(), Input{
		Series:      series,
		EvaluatedAt: series[len(series)-1].Timestamp,
		Horizon:     seasonality,
		Step:        step,
		Seasonality: seasonality,
	})
	if err != nil {
		t.Fatalf("Forecast() error = %v", err)
	}

	if result.Model != SeasonalNaiveModelName {
		t.Fatalf("model = %q, want %q after divergence fallback", result.Model, SeasonalNaiveModelName)
	}
	if result.FallbackReason != AdvisoryModelDivergence {
		t.Fatalf("fallback reason = %q, want %q", result.FallbackReason, AdvisoryModelDivergence)
	}
	if result.Reliability != ReliabilityLow {
		t.Fatalf("reliability = %q, want %q", result.Reliability, ReliabilityLow)
	}
}

func TestForecastRejectsInsufficientData(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	step := time.Minute
	series := buildSeasonalSeries(start, step, 1, []float64{10, 12, 11, 13})

	_, err := SeasonalNaiveModel{}.Forecast(context.Background(), Input{
		Series:      series,
		EvaluatedAt: series[len(series)-1].Timestamp,
		Horizon:     4 * time.Minute,
		Step:        step,
		Seasonality: 4 * time.Minute,
	})
	if err == nil {
		t.Fatal("Forecast() error = nil, want insufficient data error")
	}
	if !errors.Is(err, ErrInsufficientData) {
		t.Fatalf("Forecast() error = %v, want ErrInsufficientData", err)
	}
}

func TestSeasonalNaiveFallsBackToPersistenceWithoutSeasonality(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	step := time.Minute
	series := buildLinearSeries(start, step, 12, 50, 3)

	result, err := SeasonalNaiveModel{}.Forecast(context.Background(), Input{
		Series:      series,
		EvaluatedAt: series[len(series)-1].Timestamp,
		Horizon:     3 * time.Minute,
		Step:        step,
	})
	if err != nil {
		t.Fatalf("Forecast() error = %v", err)
	}

	last := series[len(series)-1].Value
	assertPointValues(t, result.Points, []float64{last, last, last}, 1e-9)
	if len(result.Advisories) == 0 || result.Advisories[0].Code != AdvisoryNoSeasonality {
		t.Fatalf("advisories = %#v, want no seasonality advisory", result.Advisories)
	}
	if result.Seasonality != 0 || result.SeasonalitySource != SeasonalitySourceNone {
		t.Fatalf("seasonality = %s source %q, want none", result.Seasonality, result.SeasonalitySource)
	}
}

func TestDetectSeasonalityFindsRecurringPattern(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	step := time.Minute
	series := buildSeasonalSeries(start, step, 8, []float64{100, 140, 90, 130})

	detection := DetectSeasonality(series, SeasonalityDetectionOptions{
		MinPeriod:      2 * time.Minute,
		MaxPeriod:      10 * time.Minute,
		MinCycles:      3,
		MinCorrelation: 0.75,
	})
	if !detection.Detected {
		t.Fatalf("expected detected seasonality, got %#v", detection)
	}
	if detection.Period != 4*time.Minute {
		t.Fatalf("period = %s, want 4m", detection.Period)
	}
	if detection.Confidence <= 0 {
		t.Fatalf("confidence = %.3f, want positive", detection.Confidence)
	}
}

func TestDetectSeasonalityRequiresEvidence(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	series := buildLinearSeries(start, time.Minute, 12, 50, 3)

	detection := DetectSeasonality(series, SeasonalityDetectionOptions{
		MinPeriod:      2 * time.Minute,
		MaxPeriod:      6 * time.Minute,
		MinCycles:      3,
		MinCorrelation: 0.95,
	})
	if detection.Detected {
		t.Fatalf("expected no detected seasonality, got %#v", detection)
	}
}

func TestForecastValidationTracksUnderPrediction(t *testing.T) {
	t.Parallel()

	validation := evaluateForecast(
		[]float64{100, 200, 300, 400},
		[]float64{90, 220, 240, 500},
	)
	if validation.UnderPredictedPoints != 2 {
		t.Fatalf("under-predicted points = %d, want 2", validation.UnderPredictedPoints)
	}
	if validation.UnderPredictionRate != 0.5 {
		t.Fatalf("under-prediction rate = %.2f, want 0.50", validation.UnderPredictionRate)
	}
	if math.Abs(validation.MedianUnderPredictionPct-15) > 1e-9 {
		t.Fatalf("median under-prediction = %.2f, want 15", validation.MedianUnderPredictionPct)
	}
}

func TestSideBySidePrefersTimesFMAndKeepsCandidates(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	step := time.Minute
	series := buildSeasonalSeries(start, step, 6, []float64{90, 120, 100, 130})
	result, err := SideBySideModel{
		TimesFM: staticModel{result: Result{
			Model:       TimesFMModelName,
			GeneratedAt: series[len(series)-1].Timestamp,
			Horizon:     4 * time.Minute,
			Step:        step,
			Points:      buildForecastPoints(series[len(series)-1].Timestamp, step, []float64{101, 102, 103, 104}),
			Confidence:  0.81,
			Reliability: ReliabilityHigh,
		}},
	}.Forecast(context.Background(), Input{
		Series:      series,
		EvaluatedAt: series[len(series)-1].Timestamp,
		Horizon:     4 * time.Minute,
		Step:        step,
		Seasonality: 4 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Forecast() error = %v", err)
	}
	if result.Model != TimesFMModelName {
		t.Fatalf("model = %q, want timesfm", result.Model)
	}
	if len(result.Candidates) != 3 {
		t.Fatalf("candidates = %#v, want three models", result.Candidates)
	}
	if !result.Candidates[0].Selected {
		t.Fatalf("timesfm candidate was not marked selected: %#v", result.Candidates)
	}
}

func TestCommandModelUsesJSONProtocol(t *testing.T) {
	t.Parallel()

	helper := writeTimesFMHelper(t)
	start := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	step := time.Minute
	series := buildLinearSeries(start, step, 8, 10, 2)
	result, err := CommandModel{
		Command: []string{helper},
		Timeout: 5 * time.Second,
	}.Forecast(context.Background(), Input{
		Series:      series,
		EvaluatedAt: series[len(series)-1].Timestamp,
		Horizon:     2 * time.Minute,
		Step:        step,
	})
	if err != nil {
		t.Fatalf("Forecast() error = %v", err)
	}
	if result.Model != TimesFMModelName {
		t.Fatalf("model = %q, want timesfm", result.Model)
	}
	assertPointValues(t, result.Points, []float64{26, 28}, 1e-9)
	if result.Validation.HoldoutPoints != 2 {
		t.Fatalf("holdout points = %d, want 2", result.Validation.HoldoutPoints)
	}
}

type staticModel struct {
	result Result
	err    error
}

func (m staticModel) Name() string {
	if m.result.Model != "" {
		return m.result.Model
	}
	return "static"
}

func (m staticModel) Forecast(context.Context, Input) (Result, error) {
	return m.result, m.err
}

func writeTimesFMHelper(t *testing.T) string {
	t.Helper()

	path := t.TempDir() + "/timesfm-helper.py"
	source := `#!/usr/bin/env python3
import json
import sys

payload = json.load(sys.stdin)
series = payload["series"]
horizon = payload["horizonPoints"]
step = 0
if len(series) > 1:
    step = series[-1]["value"] - series[-2]["value"]
last = series[-1]["value"]
json.dump({"model": "timesfm", "values": [last + step * (i + 1) for i in range(horizon)]}, sys.stdout)
`
	if err := os.WriteFile(path, []byte(source), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	return path
}

func buildSeasonalSeries(start time.Time, step time.Duration, seasons int, pattern []float64) []Point {
	out := make([]Point, 0, seasons*len(pattern))
	for season := 0; season < seasons; season++ {
		for offset, value := range pattern {
			index := season*len(pattern) + offset
			out = append(out, Point{
				Timestamp: start.Add(step * time.Duration(index)),
				Value:     value,
			})
		}
	}
	return out
}

func buildTrendingSeasonalSeries(start time.Time, step time.Duration, points int, pattern []float64, trendPerStep float64) []Point {
	out := make([]Point, 0, points)
	for index := 0; index < points; index++ {
		out = append(out, Point{
			Timestamp: start.Add(step * time.Duration(index)),
			Value:     100 + trendPerStep*float64(index) + pattern[index%len(pattern)],
		})
	}
	return out
}

func buildLinearSeries(start time.Time, step time.Duration, points int, intercept float64, slope float64) []Point {
	out := make([]Point, 0, points)
	for index := 0; index < points; index++ {
		out = append(out, Point{
			Timestamp: start.Add(step * time.Duration(index)),
			Value:     intercept + slope*float64(index),
		})
	}
	return out
}

func assertPointValues(t *testing.T, points []Point, expected []float64, tolerance float64) {
	t.Helper()

	if len(points) != len(expected) {
		t.Fatalf("point count = %d, want %d", len(points), len(expected))
	}
	for index, point := range points {
		if math.Abs(point.Value-expected[index]) > tolerance {
			t.Fatalf("point[%d] = %.4f, want %.4f", index, point.Value, expected[index])
		}
	}
}

func pointsToValues(points []Point) []float64 {
	out := make([]float64, 0, len(points))
	for _, point := range points {
		out = append(out, point.Value)
	}
	return out
}

func meanAbsoluteError(actual []float64, predicted []float64) float64 {
	count := min(len(actual), len(predicted))
	if count == 0 {
		return 0
	}

	var sum float64
	for index := 0; index < count; index++ {
		sum += math.Abs(actual[index] - predicted[index])
	}
	return sum / float64(count)
}
