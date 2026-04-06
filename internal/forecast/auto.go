package forecast

import (
	"context"
	"fmt"
	"math"
)

const AutoModelName = "auto"

// AutoModel runs the v1 model-selection path.
//
// Selection policy:
// - prefer seasonal naive when it is reliable enough
// - use holt-winters only when it is materially better on holdout validation
// - if the two forecasts diverge too much, fall back to the simpler seasonal naive result and lower reliability
type AutoModel struct {
	SeasonalNaive       Model
	HoltWinters         Model
	DivergenceThreshold float64
	MinimumImprovement  float64
}

func (AutoModel) Name() string {
	return AutoModelName
}

func (m AutoModel) Forecast(ctx context.Context, input Input) (Result, error) {
	seasonalModel := m.SeasonalNaive
	if seasonalModel == nil {
		seasonalModel = SeasonalNaiveModel{}
	}
	holtWintersModel := m.HoltWinters
	if holtWintersModel == nil {
		holtWintersModel = HoltWintersModel{}
	}

	seasonalResult, seasonalErr := seasonalModel.Forecast(ctx, input)
	holtWintersResult, holtWintersErr := holtWintersModel.Forecast(ctx, input)

	seasonalUsable := usable(seasonalResult, seasonalErr)
	holtWintersUsable := usable(holtWintersResult, holtWintersErr)

	switch {
	case seasonalUsable && !holtWintersUsable:
		return withUnavailableAdvisory(seasonalResult, HoltWintersModelName, holtWintersErr, holtWintersResult), nil
	case !seasonalUsable && holtWintersUsable:
		result := withUnavailableAdvisory(holtWintersResult, SeasonalNaiveModelName, seasonalErr, seasonalResult)
		result.FallbackReason = "seasonal_naive_unavailable"
		return result, nil
	case !seasonalUsable && !holtWintersUsable:
		return Result{}, fmt.Errorf(
			"%w: seasonal naive: %v; holt-winters: %v",
			ErrNoForecastResult,
			describeModelFailure(seasonalErr, seasonalResult),
			describeModelFailure(holtWintersErr, holtWintersResult),
		)
	}

	divergenceThreshold := m.DivergenceThreshold
	if divergenceThreshold <= 0 {
		divergenceThreshold = 0.35
	}
	minimumImprovement := m.MinimumImprovement
	if minimumImprovement <= 0 {
		minimumImprovement = 0.15
	}

	divergence := forecastDivergence(seasonalResult, holtWintersResult)
	if divergence > divergenceThreshold {
		seasonalResult.FallbackReason = AdvisoryModelDivergence
		seasonalResult.Advisories = append(seasonalResult.Advisories, advisory(
			AdvisoryModelDivergence,
			fmt.Sprintf(
				"seasonal naive and holt-winters diverged by %.0f%% of peak forecast value; using seasonal naive conservatively",
				divergence*100,
			),
		))
		seasonalResult.Confidence = math.Min(seasonalResult.Confidence, 0.60)
		seasonalResult.Reliability = degradeReliability(seasonalResult.Reliability, ReliabilityLow)
		return seasonalResult, nil
	}

	if shouldPreferHoltWinters(seasonalResult, holtWintersResult, minimumImprovement) {
		holtWintersResult.FallbackReason = "holt_winters_better_fit"
		return holtWintersResult, nil
	}

	return seasonalResult, nil
}

func usable(result Result, err error) bool {
	return err == nil && len(result.Points) > 0 && result.Reliability != ReliabilityUnsupported
}

func withUnavailableAdvisory(result Result, modelName string, err error, alternative Result) Result {
	message := fmt.Sprintf("%s was not available", modelName)
	switch {
	case err != nil:
		message = fmt.Sprintf("%s was not available: %v", modelName, err)
	case alternative.Reliability == ReliabilityUnsupported:
		message = fmt.Sprintf("%s produced unsupported reliability", modelName)
	}

	result.Advisories = append(result.Advisories, advisory(
		AdvisoryModelUnavailable,
		message,
	))
	return result
}

func describeModelFailure(err error, result Result) string {
	switch {
	case err != nil:
		return err.Error()
	case result.Reliability == ReliabilityUnsupported:
		return "unsupported reliability"
	default:
		return "no forecast result"
	}
}

func shouldPreferHoltWinters(seasonal Result, holtWinters Result, minimumImprovement float64) bool {
	seasonalErr := seasonal.Validation.MeanAbsoluteErr
	holtErr := holtWinters.Validation.MeanAbsoluteErr
	if seasonalErr == 0 {
		return false
	}

	requiredErr := seasonalErr * (1 - minimumImprovement)
	if holtErr > requiredErr {
		return false
	}

	return holtWinters.Confidence >= seasonal.Confidence-0.05
}

func forecastDivergence(left Result, right Result) float64 {
	points := min(len(left.Points), len(right.Points))
	if points == 0 {
		return 0
	}

	var peak float64
	for index := 0; index < points; index++ {
		numerator := math.Abs(left.Points[index].Value - right.Points[index].Value)
		denominator := math.Max(math.Max(left.Points[index].Value, right.Points[index].Value), 1)
		ratio := numerator / denominator
		if ratio > peak {
			peak = ratio
		}
	}
	return peak
}
