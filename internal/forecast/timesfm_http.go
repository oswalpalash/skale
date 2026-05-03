package forecast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPModel calls a long-running TimesFM service with the same JSON protocol as
// CommandModel. This is the preferred Kubernetes shape because Python, PyTorch,
// and model weights stay outside the controller image.
type HTTPModel struct {
	URL     string
	Timeout time.Duration
	Client  *http.Client
}

func (m HTTPModel) Name() string {
	return TimesFMModelName
}

func (m HTTPModel) Forecast(ctx context.Context, input Input) (Result, error) {
	return forecastWithTimesFMProvider(ctx, input, m.forecastValues)
}

func (m HTTPModel) forecastValues(ctx context.Context, prepared preparedInput, series []Point, horizonPoints int) ([]float64, error) {
	if strings.TrimSpace(m.URL) == "" {
		return nil, fmt.Errorf("%w: timesfm url is not configured", ErrNoForecastResult)
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
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

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, m.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: build timesfm request: %v", ErrNoForecastResult, err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := m.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: call timesfm service: %v", ErrNoForecastResult, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read timesfm response: %v", ErrNoForecastResult, err)
	}
	var response commandResponse
	if decodeErr := json.Unmarshal(body, &response); decodeErr != nil {
		return nil, fmt.Errorf("%w: decode timesfm response: %v", ErrNoForecastResult, decodeErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if response.Error != "" {
			return nil, fmt.Errorf("%w: %s", ErrNoForecastResult, response.Error)
		}
		return nil, fmt.Errorf("%w: timesfm service returned HTTP %d", ErrNoForecastResult, resp.StatusCode)
	}
	if response.Error != "" {
		return nil, fmt.Errorf("%w: %s", ErrNoForecastResult, response.Error)
	}
	values := response.Values
	if len(values) == 0 {
		return nil, fmt.Errorf("%w: timesfm response had no values", ErrNoForecastResult)
	}
	if len(values) > horizonPoints {
		values = values[:horizonPoints]
	}
	if len(values) < horizonPoints {
		return nil, fmt.Errorf("%w: timesfm returned %d values, want %d", ErrNoForecastResult, len(values), horizonPoints)
	}
	return values, nil
}
