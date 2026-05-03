package forecast

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

var (
	ErrInvalidInput     = errors.New("invalid forecast input")
	ErrInsufficientData = errors.New("insufficient data for forecast")
	ErrNoForecastResult = errors.New("no forecast result available")
	ErrModelDivergence  = errors.New("forecast models diverged")
)

type ReliabilityLevel string

const (
	ReliabilityHigh        ReliabilityLevel = "high"
	ReliabilityMedium      ReliabilityLevel = "medium"
	ReliabilityLow         ReliabilityLevel = "low"
	ReliabilityUnsupported ReliabilityLevel = "unsupported"
)

const (
	AdvisoryImplicitPersistence = "implicit_persistence"
	AdvisoryNonSeasonalMode     = "non_seasonal_mode"
	AdvisoryNoSeasonality       = "no_seasonality_detected"
	AdvisorySeasonalityDetected = "seasonality_detected"
	AdvisoryLimitedHistory      = "limited_history"
	AdvisoryModelUnavailable    = "model_unavailable"
	AdvisoryModelDivergence     = "model_divergence"
)

type SeasonalitySource string

const (
	SeasonalitySourceConfigured SeasonalitySource = "configured"
	SeasonalitySourceDetected   SeasonalitySource = "detected"
	SeasonalitySourceNone       SeasonalitySource = "none"
)

// Point is one normalized demand observation or forecast point.
type Point struct {
	Timestamp time.Time `json:"timestamp,omitempty"`
	Value     float64   `json:"value,omitempty"`
}

// Input carries the normalized demand series for a short-horizon forecast.
//
// Assumptions:
// - samples are ordered and represent one workload demand signal
// - samples are approximately evenly spaced
// - Horizon is short compared to the amount of history retained
// - Seasonality is optional; when omitted, models fall back to a one-step persistence season
type Input struct {
	Series                []Point
	EvaluatedAt           time.Time
	Horizon               time.Duration
	Step                  time.Duration
	Seasonality           time.Duration
	SeasonalitySource     SeasonalitySource
	SeasonalityConfidence float64
}

// Advisory is machine-readable forecast context that can be surfaced in status, CLI, or replay output.
type Advisory struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// Validation summarizes simple holdout-backtest quality for the chosen model.
type Validation struct {
	HoldoutPoints            int       `json:"holdoutPoints,omitempty"`
	MeanAbsoluteErr          float64   `json:"meanAbsoluteErr,omitempty"`
	MeanActual               float64   `json:"meanActual,omitempty"`
	NormalizedError          float64   `json:"normalizedError,omitempty"`
	UnderPredictedPoints     int       `json:"underPredictedPoints,omitempty"`
	UnderPredictionRate      float64   `json:"underPredictionRate,omitempty"`
	MedianUnderPredictionPct float64   `json:"medianUnderPredictionPct,omitempty"`
	UnderPredictionRatios    []float64 `json:"-"`
}

// Result captures a forecast horizon and reliability metadata.
//
// Confidence is intentionally simple: it is derived from normalized holdout MAE rather than opaque probabilistic math.
// This keeps the output explainable and deterministic for both live recommendation and replay paths.
type Result struct {
	Model                 string            `json:"model,omitempty"`
	GeneratedAt           time.Time         `json:"generatedAt,omitempty"`
	Horizon               time.Duration     `json:"horizon,omitempty"`
	Step                  time.Duration     `json:"step,omitempty"`
	Seasonality           time.Duration     `json:"seasonality,omitempty"`
	SeasonalitySource     SeasonalitySource `json:"seasonalitySource,omitempty"`
	SeasonalityConfidence float64           `json:"seasonalityConfidence,omitempty"`
	Points                []Point           `json:"points,omitempty"`
	Confidence            float64           `json:"confidence,omitempty"`
	Reliability           ReliabilityLevel  `json:"reliability,omitempty"`
	Validation            Validation        `json:"validation,omitempty"`
	FallbackReason        string            `json:"fallbackReason,omitempty"`
	Advisories            []Advisory        `json:"advisories,omitempty"`
}

// Model produces a short-horizon demand forecast from normalized demand history.
type Model interface {
	Name() string
	Forecast(ctx context.Context, input Input) (Result, error)
}

type preparedInput struct {
	series                []Point
	generatedAt           time.Time
	horizon               time.Duration
	step                  time.Duration
	horizonPoints         int
	seasonality           time.Duration
	seasonalitySource     SeasonalitySource
	seasonalityConfidence float64
	seasonPoints          int
}

func prepareInput(input Input) (preparedInput, error) {
	if err := input.Validate(); err != nil {
		return preparedInput{}, err
	}

	step := input.Step
	if step == 0 {
		step = inferStep(input.Series)
	}
	if step <= 0 {
		return preparedInput{}, fmt.Errorf("%w: forecast step must be positive", ErrInvalidInput)
	}

	horizonPoints := int(math.Ceil(float64(input.Horizon) / float64(step)))
	if horizonPoints < 1 {
		horizonPoints = 1
	}

	seasonality := input.Seasonality
	seasonalitySource := input.SeasonalitySource
	if seasonality <= 0 {
		seasonalitySource = SeasonalitySourceNone
	}
	seasonPoints := int(math.Round(float64(seasonality) / float64(step)))
	if seasonPoints < 1 {
		seasonPoints = 1
		seasonality = 0
	}
	if seasonalitySource == "" {
		if seasonality > 0 {
			seasonalitySource = SeasonalitySourceConfigured
		} else {
			seasonalitySource = SeasonalitySourceNone
		}
	}

	generatedAt := input.Series[len(input.Series)-1].Timestamp.UTC()
	if input.EvaluatedAt.After(generatedAt) {
		generatedAt = input.EvaluatedAt.UTC()
	}

	normalizedSeries := make([]Point, 0, len(input.Series))
	for _, point := range input.Series {
		normalizedSeries = append(normalizedSeries, Point{
			Timestamp: point.Timestamp.UTC(),
			Value:     point.Value,
		})
	}

	return preparedInput{
		series:                normalizedSeries,
		generatedAt:           generatedAt,
		horizon:               input.Horizon,
		step:                  step,
		horizonPoints:         horizonPoints,
		seasonality:           seasonality,
		seasonalitySource:     seasonalitySource,
		seasonalityConfidence: clamp(input.SeasonalityConfidence, 0, 1),
		seasonPoints:          seasonPoints,
	}, nil
}

// Validate checks whether the demand series is usable for deterministic short-horizon forecasting.
func (i Input) Validate() error {
	if len(i.Series) < 2 {
		return fmt.Errorf("%w: at least 2 samples are required", ErrInvalidInput)
	}
	if i.Horizon <= 0 {
		return fmt.Errorf("%w: horizon must be positive", ErrInvalidInput)
	}
	if i.Step < 0 {
		return fmt.Errorf("%w: step must not be negative", ErrInvalidInput)
	}
	if i.Seasonality < 0 {
		return fmt.Errorf("%w: seasonality must not be negative", ErrInvalidInput)
	}

	for index, point := range i.Series {
		if point.Timestamp.IsZero() {
			return fmt.Errorf("%w: sample %d has zero timestamp", ErrInvalidInput, index)
		}
		if math.IsNaN(point.Value) || math.IsInf(point.Value, 0) {
			return fmt.Errorf("%w: sample %d has invalid value", ErrInvalidInput, index)
		}
		if point.Value < 0 {
			return fmt.Errorf("%w: sample %d has negative demand", ErrInvalidInput, index)
		}
		if index > 0 && !point.Timestamp.After(i.Series[index-1].Timestamp) {
			return fmt.Errorf("%w: sample timestamps must be strictly increasing", ErrInvalidInput)
		}
	}

	return nil
}

func inferStep(series []Point) time.Duration {
	if len(series) < 2 {
		return 0
	}

	deltas := make([]int64, 0, len(series)-1)
	for index := 1; index < len(series); index++ {
		delta := series[index].Timestamp.Sub(series[index-1].Timestamp)
		if delta <= 0 {
			return 0
		}
		deltas = append(deltas, int64(delta))
	}
	sort.Slice(deltas, func(i, j int) bool {
		return deltas[i] < deltas[j]
	})

	return time.Duration(deltas[len(deltas)/2])
}

func buildForecastPoints(generatedAt time.Time, step time.Duration, values []float64) []Point {
	points := make([]Point, 0, len(values))
	for index, value := range values {
		points = append(points, Point{
			Timestamp: generatedAt.Add(step * time.Duration(index+1)).UTC(),
			Value:     value,
		})
	}
	return points
}

func evaluateForecast(actual []float64, predicted []float64) Validation {
	count := min(len(actual), len(predicted))
	if count == 0 {
		return Validation{}
	}

	var absErrSum float64
	var actualSum float64
	underPredictionRatios := make([]float64, 0, count)
	for index := 0; index < count; index++ {
		absErrSum += math.Abs(actual[index] - predicted[index])
		actualSum += actual[index]
		if actual[index] > 0 && predicted[index] < actual[index] {
			underPredictionRatios = append(underPredictionRatios, (actual[index]-predicted[index])/actual[index])
		}
	}

	mae := absErrSum / float64(count)
	meanActual := actualSum / float64(count)
	scale := math.Max(meanActual, 1)

	return Validation{
		HoldoutPoints:            count,
		MeanAbsoluteErr:          mae,
		MeanActual:               meanActual,
		NormalizedError:          mae / scale,
		UnderPredictedPoints:     len(underPredictionRatios),
		UnderPredictionRate:      float64(len(underPredictionRatios)) / float64(count),
		MedianUnderPredictionPct: medianFloat64(underPredictionRatios) * 100,
		UnderPredictionRatios:    underPredictionRatios,
	}
}

func deriveReliability(confidence float64, validation Validation) ReliabilityLevel {
	if validation.HoldoutPoints == 0 {
		return ReliabilityUnsupported
	}
	switch {
	case confidence >= 0.80:
		return ReliabilityHigh
	case confidence >= 0.60:
		return ReliabilityMedium
	case confidence >= 0.35:
		return ReliabilityLow
	default:
		return ReliabilityUnsupported
	}
}

func confidenceFromValidation(validation Validation) float64 {
	if validation.HoldoutPoints == 0 {
		return 0
	}
	confidence := 1 - validation.NormalizedError
	return clamp(confidence, 0, 1)
}

func degradeReliability(current ReliabilityLevel, limit ReliabilityLevel) ReliabilityLevel {
	order := map[ReliabilityLevel]int{
		ReliabilityHigh:        4,
		ReliabilityMedium:      3,
		ReliabilityLow:         2,
		ReliabilityUnsupported: 1,
	}
	if order[current] > order[limit] {
		return limit
	}
	return current
}

func advisory(code string, message string) Advisory {
	return Advisory{
		Code:    code,
		Message: message,
	}
}

func clamp(value float64, minValue float64, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func medianFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
