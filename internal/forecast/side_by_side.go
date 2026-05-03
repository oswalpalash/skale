package forecast

import (
	"context"
	"fmt"
)

const SideBySideModelName = "side_by_side"

// SideBySideModel runs TimesFM, seasonal naive, and Holt-Winters from one forecast boundary.
// It returns the preferred model result while preserving every candidate for UI/report comparison.
type SideBySideModel struct {
	Primary       string
	TimesFM       Model
	SeasonalNaive Model
	HoltWinters   Model
}

func (SideBySideModel) Name() string {
	return SideBySideModelName
}

func (m SideBySideModel) Forecast(ctx context.Context, input Input) (Result, error) {
	models := m.models()
	candidates := make([]CandidateResult, 0, len(models))
	results := map[string]Result{}
	errorsByModel := map[string]error{}

	for _, model := range models {
		result, err := model.Forecast(ctx, input)
		if usable(result, err) {
			results[model.Name()] = result
			candidates = append(candidates, candidateFromResult(result, false))
			continue
		}
		errorsByModel[model.Name()] = err
		candidates = append(candidates, candidateFromError(model.Name(), err))
	}

	selected, err := m.selectResult(results)
	if err != nil {
		return Result{}, err
	}
	for index := range candidates {
		candidates[index].Selected = candidates[index].Model == selected.Model
	}
	selected.Candidates = candidates

	if m.primaryName() == TimesFMModelName && selected.Model != TimesFMModelName {
		if err := errorsByModel[TimesFMModelName]; err != nil {
			selected.Advisories = append(selected.Advisories, advisory(
				AdvisoryModelUnavailable,
				fmt.Sprintf("timesfm was not available: %v", err),
			))
			selected.FallbackReason = "timesfm_unavailable"
		}
	}
	return selected, nil
}

func (m SideBySideModel) models() []Model {
	timesFM := m.TimesFM
	if timesFM == nil {
		timesFM = UnavailableModel{NameValue: TimesFMModelName, Reason: "timesfm command is not configured"}
	}
	seasonal := m.SeasonalNaive
	if seasonal == nil {
		seasonal = SeasonalNaiveModel{}
	}
	holt := m.HoltWinters
	if holt == nil {
		holt = HoltWintersModel{}
	}
	return []Model{timesFM, seasonal, holt}
}

func (m SideBySideModel) selectResult(results map[string]Result) (Result, error) {
	if result, ok := results[m.primaryName()]; ok {
		return result, nil
	}
	seasonalResult, seasonalOK := results[SeasonalNaiveModelName]
	holtResult, holtOK := results[HoltWintersModelName]
	switch {
	case seasonalOK && !holtOK:
		return seasonalResult, nil
	case !seasonalOK && holtOK:
		holtResult.FallbackReason = "seasonal_naive_unavailable"
		return holtResult, nil
	case seasonalOK && holtOK:
		if divergence := forecastDivergence(seasonalResult, holtResult); divergence > 0.35 {
			seasonalResult.FallbackReason = AdvisoryModelDivergence
			seasonalResult.Advisories = append(seasonalResult.Advisories, advisory(
				AdvisoryModelDivergence,
				fmt.Sprintf(
					"seasonal naive and holt-winters diverged by %.0f%% of peak forecast value; using seasonal naive conservatively",
					divergence*100,
				),
			))
			seasonalResult.Reliability = degradeReliability(seasonalResult.Reliability, ReliabilityLow)
			return seasonalResult, nil
		}
		if shouldPreferHoltWinters(seasonalResult, holtResult, 0.15) {
			holtResult.FallbackReason = "holt_winters_better_fit"
			return holtResult, nil
		}
		return seasonalResult, nil
	}
	return Result{}, fmt.Errorf("%w: no side-by-side forecast model produced a usable result", ErrNoForecastResult)
}

func (m SideBySideModel) primaryName() string {
	if m.Primary != "" {
		return m.Primary
	}
	return TimesFMModelName
}

// UnavailableModel is useful for side-by-side UI when an optional model is not configured.
type UnavailableModel struct {
	NameValue string
	Reason    string
}

func (m UnavailableModel) Name() string {
	if m.NameValue != "" {
		return m.NameValue
	}
	return "unavailable"
}

func (m UnavailableModel) Forecast(context.Context, Input) (Result, error) {
	reason := m.Reason
	if reason == "" {
		reason = "model is not configured"
	}
	return Result{}, fmt.Errorf("%w: %s", ErrNoForecastResult, reason)
}
