package prometheus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHTTPAPIQueryRangeDecodesFixture(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "query_range_success.json")
	var captured url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(server.Close)

	api := HTTPAPI{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	}

	start := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	result, err := api.QueryRange(context.Background(), `sum(rate(http_requests_total[5m]))`, start, end, 30*time.Second)
	if err != nil {
		t.Fatalf("QueryRange() error = %v", err)
	}

	if got := captured.Get("query"); got != `sum(rate(http_requests_total[5m]))` {
		t.Fatalf("query param = %q, want %q", got, `sum(rate(http_requests_total[5m]))`)
	}
	if got := captured.Get("step"); got != "30" {
		t.Fatalf("step param = %q, want %q", got, "30")
	}
	if len(result.Series) != 1 {
		t.Fatalf("series count = %d, want 1", len(result.Series))
	}
	if len(result.Series[0].Samples) != 2 {
		t.Fatalf("sample count = %d, want 2", len(result.Series[0].Samples))
	}
	if got := result.Series[0].Samples[0].Value; got != 120.5 {
		t.Fatalf("first sample value = %v, want %v", got, 120.5)
	}
}

func TestHTTPAPIQueryRangeRejectsMalformedFixture(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "query_range_malformed_value.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(server.Close)

	api := HTTPAPI{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	}

	start := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	_, err := api.QueryRange(context.Background(), `sum(rate(http_requests_total[5m]))`, start, end, 30*time.Second)
	if err == nil {
		t.Fatal("QueryRange() error = nil, want malformed response error")
	}
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("QueryRange() error = %v, want ErrMalformedResponse", err)
	}
}

func TestHTTPAPIQueryRangeSkipsStaleNaNSamples(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {
				"resultType": "matrix",
				"result": [{
					"metric": {"__name__": "skale_recommendation_recommended_replicas"},
					"values": [[1777833590, "NaN"], [1777833620, "4"]]
				}]
			}
		}`))
	}))
	t.Cleanup(server.Close)

	api := HTTPAPI{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	}
	start := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	result, err := api.QueryRange(context.Background(), `skale_recommendation_recommended_replicas`, start, end, 30*time.Second)
	if err != nil {
		t.Fatalf("QueryRange() error = %v", err)
	}
	if got, want := len(result.Series[0].Samples), 1; got != want {
		t.Fatalf("sample count = %d, want %d", got, want)
	}
	if got := result.Series[0].Samples[0].Value; got != 4 {
		t.Fatalf("remaining sample value = %v, want 4", got)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	bytes, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", name, err)
	}
	return bytes
}
