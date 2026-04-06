package forecast

import (
	"context"
	"fmt"
	"math"
)

const HoltWintersModelName = "holt_winters"

// HoltWintersModel implements a small additive Holt-Winters forecaster with a fixed deterministic
// parameter search grid. The search space is intentionally narrow so the model remains debuggable
// and cheap enough for replay and live recommendation use.
//
// Limitations:
// - additive seasonality only
// - assumes roughly even sample spacing
// - not intended for chaotic one-off spikes
type HoltWintersModel struct {
	Alphas []float64
	Betas  []float64
	Gammas []float64
}

func (HoltWintersModel) Name() string {
	return HoltWintersModelName
}

func (m HoltWintersModel) Forecast(_ context.Context, input Input) (Result, error) {
	prepared, err := prepareInput(input)
	if err != nil {
		return Result{}, err
	}

	minTraining := max(prepared.seasonPoints+2, prepared.seasonPoints*2)
	requiredSamples := minTraining + prepared.horizonPoints
	if len(prepared.series) < requiredSamples {
		return Result{}, fmt.Errorf(
			"%w: holt-winters requires at least %d samples for %d-step horizon and %d-step season",
			ErrInsufficientData,
			requiredSamples,
			prepared.horizonPoints,
			prepared.seasonPoints,
		)
	}

	trainEnd := len(prepared.series) - prepared.horizonPoints
	trainValues := values(prepared.series[:trainEnd])
	holdoutActual := values(prepared.series[trainEnd:])

	bestParams, validationPredicted, err := m.bestParameters(trainValues, prepared.seasonPoints, holdoutActual)
	if err != nil {
		return Result{}, err
	}
	forecastValues, err := holtWintersForecast(values(prepared.series), prepared.seasonPoints, prepared.horizonPoints, bestParams)
	if err != nil {
		return Result{}, err
	}

	validation := evaluateForecast(holdoutActual, validationPredicted)
	confidence := confidenceFromValidation(validation)
	reliability := deriveReliability(confidence, validation)

	result := Result{
		Model:       HoltWintersModelName,
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
			AdvisoryNonSeasonalMode,
			"seasonality was not provided; Holt-Winters ran in non-seasonal trend mode",
		))
		result.Reliability = degradeReliability(result.Reliability, ReliabilityMedium)
	}
	if len(prepared.series) < prepared.seasonPoints*3 {
		result.Advisories = append(result.Advisories, advisory(
			AdvisoryLimitedHistory,
			"history covers fewer than three full seasons; holt-winters parameters were fit on limited data",
		))
		result.Reliability = degradeReliability(result.Reliability, ReliabilityLow)
	}

	return result, nil
}

type holtWintersParams struct {
	alpha float64
	beta  float64
	gamma float64
}

func (m HoltWintersModel) bestParameters(series []float64, seasonPoints int, holdoutActual []float64) (holtWintersParams, []float64, error) {
	var (
		bestParams   holtWintersParams
		bestForecast []float64
		bestScore    = math.Inf(1)
		bestSet      bool
	)

	horizonPoints := len(holdoutActual)
	for _, params := range m.parameterGrid(seasonPoints) {
		forecastValues, err := holtWintersForecast(series, seasonPoints, horizonPoints, params)
		if err != nil {
			continue
		}
		score := evaluateForecast(holdoutActual, forecastValues).MeanAbsoluteErr
		if score < bestScore {
			bestScore = score
			bestParams = params
			bestForecast = forecastValues
			bestSet = true
		}
	}

	if !bestSet {
		return holtWintersParams{}, nil, fmt.Errorf("%w: no usable holt-winters parameter set", ErrNoForecastResult)
	}

	return bestParams, bestForecast, nil
}

func (m HoltWintersModel) parameterGrid(seasonPoints int) []holtWintersParams {
	alphas := sanitizeGrid(m.Alphas, []float64{0.2, 0.4, 0.6})
	betas := sanitizeGrid(m.Betas, []float64{0.1, 0.2, 0.4})
	gammas := sanitizeGrid(m.Gammas, []float64{0.2, 0.4, 0.6})
	if seasonPoints == 1 {
		gammas = []float64{0}
	}

	grid := make([]holtWintersParams, 0, len(alphas)*len(betas)*len(gammas))
	for _, alpha := range alphas {
		for _, beta := range betas {
			for _, gamma := range gammas {
				grid = append(grid, holtWintersParams{
					alpha: alpha,
					beta:  beta,
					gamma: gamma,
				})
			}
		}
	}
	return grid
}

func sanitizeGrid(values []float64, defaults []float64) []float64 {
	if len(values) == 0 {
		return defaults
	}
	out := make([]float64, 0, len(values))
	for _, value := range values {
		if value > 0 && value < 1 {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return defaults
	}
	return out
}

func holtWintersForecast(series []float64, seasonPoints int, horizonPoints int, params holtWintersParams) ([]float64, error) {
	switch {
	case len(series) < 2:
		return nil, fmt.Errorf("%w: holt-winters needs at least 2 points", ErrInsufficientData)
	case seasonPoints < 1:
		return nil, fmt.Errorf("%w: season points must be positive", ErrInvalidInput)
	case horizonPoints < 1:
		return nil, fmt.Errorf("%w: horizon points must be positive", ErrInvalidInput)
	}

	if seasonPoints == 1 {
		return holtLinearForecast(series, horizonPoints, params), nil
	}
	if len(series) < seasonPoints*2 {
		return nil, fmt.Errorf("%w: holt-winters needs at least 2 full seasons", ErrInsufficientData)
	}

	level, trend, seasonals := initializeAdditiveState(series, seasonPoints)
	for index := seasonPoints; index < len(series); index++ {
		seasonIndex := index % seasonPoints
		season := seasonals[seasonIndex]
		prevLevel := level
		level = params.alpha*(series[index]-season) + (1-params.alpha)*(level+trend)
		trend = params.beta*(level-prevLevel) + (1-params.beta)*trend
		seasonals[seasonIndex] = params.gamma*(series[index]-level) + (1-params.gamma)*season
	}

	forecastValues := make([]float64, 0, horizonPoints)
	for step := 1; step <= horizonPoints; step++ {
		seasonIndex := (len(series) + step - 1) % seasonPoints
		value := level + float64(step)*trend + seasonals[seasonIndex]
		forecastValues = append(forecastValues, math.Max(0, value))
	}
	return forecastValues, nil
}

func initializeAdditiveState(series []float64, seasonPoints int) (float64, float64, []float64) {
	firstSeasonAvg := average(series[:seasonPoints])
	secondSeasonAvg := average(series[seasonPoints : seasonPoints*2])
	seasonals := make([]float64, seasonPoints)
	for index := 0; index < seasonPoints; index++ {
		seasonals[index] = series[index] - firstSeasonAvg
	}

	level := firstSeasonAvg
	trend := (secondSeasonAvg - firstSeasonAvg) / float64(seasonPoints)
	return level, trend, seasonals
}

func holtLinearForecast(series []float64, horizonPoints int, params holtWintersParams) []float64 {
	level := series[0]
	trend := series[1] - series[0]

	for index := 1; index < len(series); index++ {
		prevLevel := level
		level = params.alpha*series[index] + (1-params.alpha)*(level+trend)
		trend = params.beta*(level-prevLevel) + (1-params.beta)*trend
	}

	forecastValues := make([]float64, 0, horizonPoints)
	for step := 1; step <= horizonPoints; step++ {
		forecastValues = append(forecastValues, math.Max(0, level+float64(step)*trend))
	}
	return forecastValues
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
