package forecast

import (
	"context"
	"fmt"
)

const SeasonalNaiveModelName = "seasonal_naive"

// SeasonalNaiveModel repeats the most recent observed season into the forecast horizon.
//
// This is the preferred v1 baseline because it is simple, deterministic, and easy to explain.
// When Seasonality is omitted, the model degrades to one-step persistence by repeating the last observed point.
type SeasonalNaiveModel struct{}

func (SeasonalNaiveModel) Name() string {
	return SeasonalNaiveModelName
}

func (SeasonalNaiveModel) Forecast(_ context.Context, input Input) (Result, error) {
	prepared, err := prepareInput(input)
	if err != nil {
		return Result{}, err
	}

	requiredSamples := prepared.seasonPoints + prepared.horizonPoints
	if len(prepared.series) < requiredSamples {
		return Result{}, fmt.Errorf(
			"%w: seasonal naive requires at least %d samples for %d-step horizon and %d-step season",
			ErrInsufficientData,
			requiredSamples,
			prepared.horizonPoints,
			prepared.seasonPoints,
		)
	}

	forecastValues := seasonalNaiveValues(prepared.series, prepared.seasonPoints, prepared.horizonPoints)
	holdoutStart := len(prepared.series) - prepared.horizonPoints
	validationActual := values(prepared.series[holdoutStart:])
	validationPredicted := seasonalNaiveValues(prepared.series[:holdoutStart], prepared.seasonPoints, prepared.horizonPoints)
	validation := evaluateForecast(validationActual, validationPredicted)
	confidence := confidenceFromValidation(validation)
	reliability := deriveReliability(confidence, validation)

	result := Result{
		Model:       SeasonalNaiveModelName,
		GeneratedAt: prepared.generatedAt,
		Horizon:     prepared.horizon,
		Step:        prepared.step,
		Seasonality: prepared.seasonality,
		Points:      buildForecastPoints(prepared.generatedAt, prepared.step, forecastValues),
		Confidence:  confidence,
		Reliability: reliability,
		Validation:  validation,
	}

	if prepared.seasonPoints == 1 {
		result.Advisories = append(result.Advisories, advisory(
			AdvisoryImplicitPersistence,
			"seasonality was not provided; seasonal naive fell back to repeating the most recent point",
		))
		result.Reliability = degradeReliability(result.Reliability, ReliabilityMedium)
	}
	if len(prepared.series) < prepared.seasonPoints*2 {
		result.Advisories = append(result.Advisories, advisory(
			AdvisoryLimitedHistory,
			"history covers less than two full seasons; confidence is based on a shorter backtest",
		))
		result.Reliability = degradeReliability(result.Reliability, ReliabilityLow)
	}

	return result, nil
}

func seasonalNaiveValues(series []Point, seasonPoints int, horizonPoints int) []float64 {
	values := make([]float64, 0, horizonPoints)
	start := len(series) - seasonPoints
	for index := 0; index < horizonPoints; index++ {
		values = append(values, series[start+(index%seasonPoints)].Value)
	}
	return values
}

func values(points []Point) []float64 {
	out := make([]float64, 0, len(points))
	for _, point := range points {
		out = append(out, point.Value)
	}
	return out
}
