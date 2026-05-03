package forecast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

const TimesFMModelName = "timesfm"

// CommandModel calls an external TimesFM runner using a small JSON stdin/stdout protocol.
// This keeps model weights and Python runtime concerns outside the Go controller binary.
type CommandModel struct {
	Command []string
	Timeout time.Duration
}

func (m CommandModel) Name() string {
	return TimesFMModelName
}

func (m CommandModel) Forecast(ctx context.Context, input Input) (Result, error) {
	prepared, err := prepareInput(input)
	if err != nil {
		return Result{}, err
	}
	if len(m.Command) == 0 || m.Command[0] == "" {
		return Result{}, fmt.Errorf("%w: timesfm command is not configured", ErrNoForecastResult)
	}

	forecastValues, err := m.forecastValues(ctx, prepared, prepared.series, prepared.horizonPoints)
	if err != nil {
		return Result{}, err
	}
	validation := Validation{}
	if len(prepared.series) > prepared.horizonPoints+1 {
		holdoutStart := len(prepared.series) - prepared.horizonPoints
		validationValues, err := m.forecastValues(ctx, prepared, prepared.series[:holdoutStart], prepared.horizonPoints)
		if err == nil {
			validation = evaluateForecast(values(prepared.series[holdoutStart:]), validationValues)
		}
	}
	confidence := confidenceFromValidation(validation)
	if validation.HoldoutPoints == 0 {
		confidence = 0.70
	}
	reliability := deriveReliability(confidence, validation)
	if validation.HoldoutPoints == 0 {
		reliability = ReliabilityMedium
	}

	result := Result{
		Model:                 TimesFMModelName,
		GeneratedAt:           prepared.generatedAt,
		Horizon:               prepared.horizon,
		Step:                  prepared.step,
		Seasonality:           prepared.seasonality,
		SeasonalitySource:     prepared.seasonalitySource,
		SeasonalityConfidence: prepared.seasonalityConfidence,
		Points:                buildForecastPoints(prepared.generatedAt, prepared.step, forecastValues),
		Confidence:            confidence,
		Reliability:           reliability,
		Validation:            validation,
	}
	if validation.HoldoutPoints == 0 {
		result.Advisories = append(result.Advisories, advisory(
			AdvisoryLimitedHistory,
			"timesfm did not have enough holdout points for local confidence calibration",
		))
	}
	return result, nil
}

func (m CommandModel) forecastValues(ctx context.Context, prepared preparedInput, series []Point, horizonPoints int) ([]float64, error) {
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request := commandRequest{
		Model:         TimesFMModelName,
		EvaluatedAt:   prepared.generatedAt,
		StepSeconds:   prepared.step.Seconds(),
		HorizonPoints: horizonPoints,
		Series:        series,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("%w: encode timesfm request: %v", ErrInvalidInput, err)
	}

	command := exec.CommandContext(commandCtx, m.Command[0], m.Command[1:]...)
	command.Stdin = bytes.NewReader(payload)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		var response commandResponse
		if decodeErr := json.Unmarshal(stdout.Bytes(), &response); decodeErr == nil && response.Error != "" {
			return nil, fmt.Errorf("%w: %s", ErrNoForecastResult, response.Error)
		}
		return nil, fmt.Errorf("%w: run timesfm command: %v: %s", ErrNoForecastResult, err, stderr.String())
	}

	var response commandResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return nil, fmt.Errorf("%w: decode timesfm response: %v", ErrNoForecastResult, err)
	}
	if response.Error != "" {
		return nil, fmt.Errorf("%w: %s", ErrNoForecastResult, response.Error)
	}
	if len(response.Values) == 0 {
		return nil, fmt.Errorf("%w: timesfm response had no values", ErrNoForecastResult)
	}
	values := response.Values
	if len(values) > horizonPoints {
		values = values[:horizonPoints]
	}
	if len(values) < horizonPoints {
		return nil, fmt.Errorf("%w: timesfm returned %d values, want %d", ErrNoForecastResult, len(values), horizonPoints)
	}
	return values, nil
}

type commandRequest struct {
	Model         string    `json:"model,omitempty"`
	EvaluatedAt   time.Time `json:"evaluatedAt,omitempty"`
	StepSeconds   float64   `json:"stepSeconds,omitempty"`
	HorizonPoints int       `json:"horizonPoints,omitempty"`
	Series        []Point   `json:"series,omitempty"`
}

type commandResponse struct {
	Model  string    `json:"model,omitempty"`
	Values []float64 `json:"values,omitempty"`
	Error  string    `json:"error,omitempty"`
}
