package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
)

var (
	ErrInvalidQueryWindow  = errors.New("invalid prometheus query window")
	ErrMalformedResponse   = errors.New("malformed prometheus response")
	ErrUnexpectedResponse  = errors.New("unexpected prometheus response")
	ErrPrometheusAPIStatus = errors.New("prometheus query failed")
)

// API abstracts Prometheus range queries so adapters remain easy to stub in tests.
type API interface {
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (RangeQueryResult, error)
}

// RangeQueryResult is the minimal typed shape the adapter needs from Prometheus.
type RangeQueryResult struct {
	Series []QuerySeries
}

// QuerySeries is one normalized Prometheus matrix series.
type QuerySeries struct {
	Labels  map[string]string
	Samples []metrics.Sample
}

// HTTPAPI executes Prometheus query_range requests over HTTP.
type HTTPAPI struct {
	BaseURL    string
	HTTPClient *http.Client
}

// QueryRange fetches a Prometheus matrix result and converts it into typed series data.
func (c HTTPAPI) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (RangeQueryResult, error) {
	if strings.TrimSpace(query) == "" {
		return RangeQueryResult{}, fmt.Errorf("%w: query must not be empty", ErrUnexpectedResponse)
	}
	if start.IsZero() || end.IsZero() || !end.After(start) || step <= 0 {
		return RangeQueryResult{}, ErrInvalidQueryWindow
	}

	baseURL, err := url.Parse(c.BaseURL)
	if err != nil {
		return RangeQueryResult{}, fmt.Errorf("%w: parse base url: %v", ErrUnexpectedResponse, err)
	}

	baseURL.Path = path.Join(baseURL.Path, "/api/v1/query_range")
	params := baseURL.Query()
	params.Set("query", query)
	params.Set("start", strconv.FormatFloat(float64(start.Unix()), 'f', -1, 64))
	params.Set("end", strconv.FormatFloat(float64(end.Unix()), 'f', -1, 64))
	params.Set("step", strconv.FormatFloat(step.Seconds(), 'f', -1, 64))
	baseURL.RawQuery = params.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL.String(), nil)
	if err != nil {
		return RangeQueryResult{}, fmt.Errorf("%w: build request: %v", ErrUnexpectedResponse, err)
	}

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	response, err := client.Do(request)
	if err != nil {
		return RangeQueryResult{}, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return RangeQueryResult{}, fmt.Errorf("%w: http status %d", ErrPrometheusAPIStatus, response.StatusCode)
	}

	var payload queryRangeResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return RangeQueryResult{}, fmt.Errorf("%w: decode response: %v", ErrMalformedResponse, err)
	}

	if payload.Status != "success" {
		message := strings.TrimSpace(payload.Error)
		if message == "" {
			message = "unknown error"
		}
		return RangeQueryResult{}, fmt.Errorf("%w: %s", ErrPrometheusAPIStatus, message)
	}
	if payload.Data.ResultType != "matrix" {
		return RangeQueryResult{}, fmt.Errorf("%w: expected matrix result, got %q", ErrUnexpectedResponse, payload.Data.ResultType)
	}

	result := RangeQueryResult{
		Series: make([]QuerySeries, 0, len(payload.Data.Result)),
	}

	for _, rawSeries := range payload.Data.Result {
		series := QuerySeries{
			Labels:  rawSeries.Metric,
			Samples: make([]metrics.Sample, 0, len(rawSeries.Values)),
		}
		for _, sample := range rawSeries.Values {
			series.Samples = append(series.Samples, sample.Sample)
		}
		result.Series = append(result.Series, series)
	}

	return result, nil
}

type queryRangeResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string           `json:"resultType"`
		Result     []rawQuerySeries `json:"result"`
	} `json:"data"`
}

type rawQuerySeries struct {
	Metric map[string]string `json:"metric"`
	Values []rawSample       `json:"values"`
}

type rawSample struct {
	metrics.Sample
}

func (s *rawSample) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%w: decode sample: %v", ErrMalformedResponse, err)
	}
	if len(raw) != 2 {
		return fmt.Errorf("%w: expected sample pair, got %d fields", ErrMalformedResponse, len(raw))
	}

	var timestamp float64
	if err := json.Unmarshal(raw[0], &timestamp); err != nil {
		return fmt.Errorf("%w: decode sample timestamp: %v", ErrMalformedResponse, err)
	}
	if math.IsNaN(timestamp) || math.IsInf(timestamp, 0) {
		return fmt.Errorf("%w: invalid sample timestamp", ErrMalformedResponse)
	}

	var valueString string
	if err := json.Unmarshal(raw[1], &valueString); err != nil {
		return fmt.Errorf("%w: decode sample value: %v", ErrMalformedResponse, err)
	}

	value, err := strconv.ParseFloat(valueString, 64)
	if err != nil {
		return fmt.Errorf("%w: parse sample value %q: %v", ErrMalformedResponse, valueString, err)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("%w: invalid sample value %q", ErrMalformedResponse, valueString)
	}

	s.Sample = metrics.Sample{
		Timestamp: time.Unix(int64(timestamp), 0).UTC(),
		Value:     value,
	}
	return nil
}
