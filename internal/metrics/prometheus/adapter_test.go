package prometheus

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
)

func TestAdapterLoadWindowSuccess(t *testing.T) {
	t.Parallel()

	window := metrics.Window{
		Start: time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC),
		End:   time.Date(2026, time.April, 2, 12, 10, 0, 0, time.UTC),
	}
	target := metrics.Target{
		Namespace: "payments",
		Name:      "checkout",
	}

	api := &fakeAPI{
		results: map[string]RangeQueryResult{
			`sum(rate(http_requests_total{namespace="payments",deployment="checkout"}[5m]))`:                                                    singleSeriesResult(120.5, 133.0),
			`max(kube_deployment_status_replicas{namespace="payments",deployment="checkout"})`:                                                  singleSeriesResult(4, 4),
			`avg(container_cpu_saturation{namespace="payments",deployment="checkout"})`:                                                         singleSeriesResult(0.72, 0.77),
			`avg(container_memory_saturation{namespace="payments",deployment="checkout"})`:                                                      singleSeriesResult(0.63, 0.66),
			`histogram_quantile(0.95, sum(rate(http_request_duration_seconds_bucket{namespace="payments",deployment="checkout"}[5m])) by (le))`: singleSeriesResult(0.35, 0.42),
			`sum(rate(http_requests_errors_total{namespace="payments",deployment="checkout"}[5m]))`:                                             singleSeriesResult(1.2, 1.4),
			`avg(pod_startup_seconds{namespace="payments",deployment="checkout"})`:                                                              singleSeriesResult(38, 40),
			`sum(cluster_schedulable_replicas{namespace="payments"})`:                                                                           singleSeriesResult(6, 6),
		},
	}

	adapter := Adapter{
		API: api,
		Queries: Queries{
			Demand: SignalQuery{
				Expr: `sum(rate(http_requests_total{namespace="$namespace",deployment="$deployment"}[5m]))`,
				Unit: "rps",
			},
			Replicas: SignalQuery{
				Expr: `max(kube_deployment_status_replicas{namespace="$namespace",deployment="$deployment"})`,
				Unit: "replicas",
			},
			CPU: SignalQuery{
				Expr: `avg(container_cpu_saturation{namespace="$namespace",deployment="$deployment"})`,
				Unit: "ratio",
			},
			Memory: SignalQuery{
				Expr: `avg(container_memory_saturation{namespace="$namespace",deployment="$deployment"})`,
				Unit: "ratio",
			},
			Latency: SignalQuery{
				Expr: `histogram_quantile(0.95, sum(rate(http_request_duration_seconds_bucket{namespace="$namespace",deployment="$deployment"}[5m])) by (le))`,
				Unit: "seconds",
			},
			Errors: SignalQuery{
				Expr: `sum(rate(http_requests_errors_total{namespace="$namespace",deployment="$deployment"}[5m]))`,
				Unit: "rps",
			},
			Warmup: SignalQuery{
				Expr: `avg(pod_startup_seconds{namespace="$namespace",deployment="$deployment"})`,
				Unit: "seconds",
			},
			NodeHeadroom: SignalQuery{
				Expr: `sum(cluster_schedulable_replicas{namespace="$namespace"})`,
				Unit: "replicas",
			},
		},
		Step: 30 * time.Second,
	}

	snapshot, err := adapter.LoadWindow(context.Background(), target, window)
	if err != nil {
		t.Fatalf("LoadWindow() error = %v", err)
	}

	expectedQueries := []string{
		`sum(rate(http_requests_total{namespace="payments",deployment="checkout"}[5m]))`,
		`max(kube_deployment_status_replicas{namespace="payments",deployment="checkout"})`,
		`avg(container_cpu_saturation{namespace="payments",deployment="checkout"})`,
		`avg(container_memory_saturation{namespace="payments",deployment="checkout"})`,
		`histogram_quantile(0.95, sum(rate(http_request_duration_seconds_bucket{namespace="payments",deployment="checkout"}[5m])) by (le))`,
		`sum(rate(http_requests_errors_total{namespace="payments",deployment="checkout"}[5m]))`,
		`avg(pod_startup_seconds{namespace="payments",deployment="checkout"})`,
		`sum(cluster_schedulable_replicas{namespace="payments"})`,
	}
	if !reflect.DeepEqual(api.queries, expectedQueries) {
		t.Fatalf("queried expressions mismatch\nwant: %#v\ngot:  %#v", expectedQueries, api.queries)
	}

	if snapshot.Demand.Unit != "rps" {
		t.Fatalf("demand unit = %q, want %q", snapshot.Demand.Unit, "rps")
	}
	if snapshot.Replicas.Unit != "replicas" {
		t.Fatalf("replicas unit = %q, want %q", snapshot.Replicas.Unit, "replicas")
	}
	if snapshot.CPU == nil || snapshot.Memory == nil || snapshot.Warmup == nil {
		t.Fatalf("expected cpu, memory, and warmup signals to be populated")
	}
	if snapshot.Latency == nil || snapshot.Errors == nil {
		t.Fatalf("expected latency and error signals to be populated")
	}
	if snapshot.NodeHeadroom == nil {
		t.Fatalf("expected node headroom signal to be populated")
	}

	labelSignature := "deployment=checkout,namespace=payments"
	if !reflect.DeepEqual(snapshot.Demand.ObservedLabelSignatures, []string{labelSignature}) {
		t.Fatalf("observed label signatures = %#v, want %#v", snapshot.Demand.ObservedLabelSignatures, []string{labelSignature})
	}
}

func TestAdapterLoadWindowAllowsMissingOptionalSignals(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{
		results: map[string]RangeQueryResult{
			`sum(rate(http_requests_total{namespace="payments",deployment="checkout"}[5m]))`:   singleSeriesResult(120.5, 133.0),
			`max(kube_deployment_status_replicas{namespace="payments",deployment="checkout"})`: singleSeriesResult(4, 4),
		},
	}

	adapter := Adapter{
		API: api,
		Queries: Queries{
			Demand: SignalQuery{
				Expr: `sum(rate(http_requests_total{namespace="$namespace",deployment="$deployment"}[5m]))`,
			},
			Replicas: SignalQuery{
				Expr: `max(kube_deployment_status_replicas{namespace="$namespace",deployment="$deployment"})`,
			},
			Latency: SignalQuery{
				Expr: `sum(rate(http_request_duration_seconds_count{namespace="$namespace",deployment="$deployment"}[5m]))`,
			},
		},
	}

	snapshot, err := adapter.LoadWindow(context.Background(), metrics.Target{Namespace: "payments", Name: "checkout"}, testWindow())
	if err != nil {
		t.Fatalf("LoadWindow() error = %v", err)
	}

	if snapshot.Latency != nil {
		t.Fatalf("expected missing optional latency signal to be nil")
	}
	if snapshot.CPU != nil || snapshot.NodeHeadroom != nil {
		t.Fatalf("expected unspecified optional signals to be nil")
	}
}

func TestAdapterLoadWindowFailsForMissingRequiredSeries(t *testing.T) {
	t.Parallel()

	adapter := Adapter{
		API: &fakeAPI{},
		Queries: Queries{
			Demand: SignalQuery{
				Expr: `sum(rate(http_requests_total{namespace="$namespace",deployment="$deployment"}[5m]))`,
			},
			Replicas: SignalQuery{
				Expr: `max(kube_deployment_status_replicas{namespace="$namespace",deployment="$deployment"})`,
			},
		},
	}

	_, err := adapter.LoadWindow(context.Background(), metrics.Target{Namespace: "payments", Name: "checkout"}, testWindow())
	if err == nil {
		t.Fatal("LoadWindow() error = nil, want missing demand error")
	}
	if !errors.Is(err, ErrSeriesMissing) {
		t.Fatalf("LoadWindow() error = %v, want ErrSeriesMissing", err)
	}

	var signalErr *SignalError
	if !errors.As(err, &signalErr) {
		t.Fatalf("LoadWindow() error = %v, want SignalError", err)
	}
	if signalErr.Signal != metrics.SignalDemand {
		t.Fatalf("signal error = %s, want %s", signalErr.Signal, metrics.SignalDemand)
	}
}

func TestAdapterLoadWindowFailsForAmbiguousSeries(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{
		results: map[string]RangeQueryResult{
			`sum(rate(http_requests_total{namespace="payments",deployment="checkout"}[5m]))`: singleSeriesResult(120.5, 133.0),
			`max(kube_deployment_status_replicas{namespace="payments",deployment="checkout"})`: {
				Series: []QuerySeries{
					querySeries("payments", "checkout", 4, 4),
					querySeries("payments", "checkout-canary", 1, 1),
				},
			},
		},
	}

	adapter := Adapter{
		API: api,
		Queries: Queries{
			Demand: SignalQuery{
				Expr: `sum(rate(http_requests_total{namespace="$namespace",deployment="$deployment"}[5m]))`,
			},
			Replicas: SignalQuery{
				Expr: `max(kube_deployment_status_replicas{namespace="$namespace",deployment="$deployment"})`,
			},
		},
	}

	_, err := adapter.LoadWindow(context.Background(), metrics.Target{Namespace: "payments", Name: "checkout"}, testWindow())
	if err == nil {
		t.Fatal("LoadWindow() error = nil, want ambiguous series error")
	}
	if !errors.Is(err, ErrAmbiguousSeries) {
		t.Fatalf("LoadWindow() error = %v, want ErrAmbiguousSeries", err)
	}
}

func TestAdapterLoadWindowFailsForMalformedSeries(t *testing.T) {
	t.Parallel()

	window := testWindow()
	api := &fakeAPI{
		results: map[string]RangeQueryResult{
			`sum(rate(http_requests_total{namespace="payments",deployment="checkout"}[5m]))`: {
				Series: []QuerySeries{{
					Labels: map[string]string{
						"namespace":  "payments",
						"deployment": "checkout",
					},
					Samples: []metrics.Sample{
						{Timestamp: window.Start.Add(30 * time.Second), Value: 120},
						{Timestamp: window.Start.Add(30 * time.Second), Value: 130},
					},
				}},
			},
			`max(kube_deployment_status_replicas{namespace="payments",deployment="checkout"})`: singleSeriesResult(4, 4),
		},
	}

	adapter := Adapter{
		API: api,
		Queries: Queries{
			Demand: SignalQuery{
				Expr: `sum(rate(http_requests_total{namespace="$namespace",deployment="$deployment"}[5m]))`,
			},
			Replicas: SignalQuery{
				Expr: `max(kube_deployment_status_replicas{namespace="$namespace",deployment="$deployment"})`,
			},
		},
	}

	_, err := adapter.LoadWindow(context.Background(), metrics.Target{Namespace: "payments", Name: "checkout"}, window)
	if err == nil {
		t.Fatal("LoadWindow() error = nil, want malformed series error")
	}
	if !errors.Is(err, ErrMalformedSeries) {
		t.Fatalf("LoadWindow() error = %v, want ErrMalformedSeries", err)
	}
}

func TestAdapterLoadRecommendationHistory(t *testing.T) {
	t.Parallel()

	window := testWindow()
	api := &fakeAPI{
		results: map[string]RangeQueryResult{
			`skale_recommendation_recommended_replicas{namespace="payments",workload="checkout"}`: {
				Series: []QuerySeries{{
					Labels: map[string]string{
						"namespace": "payments",
						"workload":  "checkout",
						"policy":    "checkout-policy",
						"state":     "available",
					},
					Samples: []metrics.Sample{
						{Timestamp: window.Start.Add(2 * time.Minute), Value: 4},
						{Timestamp: window.Start.Add(3 * time.Minute), Value: 5},
					},
				}, {
					Labels: map[string]string{
						"namespace": "payments",
						"workload":  "checkout",
						"policy":    "checkout-policy",
						"state":     "suppressed",
					},
					Samples: []metrics.Sample{
						{Timestamp: window.Start.Add(time.Minute), Value: 3},
					},
				}},
			},
		},
	}
	adapter := Adapter{
		API:  api,
		Step: 30 * time.Second,
	}

	history, err := adapter.LoadRecommendationHistory(context.Background(), metrics.Target{Namespace: "payments", Name: "checkout"}, window)
	if err != nil {
		t.Fatalf("LoadRecommendationHistory() error = %v", err)
	}
	if got, want := len(history), 3; got != want {
		t.Fatalf("history length = %d, want %d: %#v", got, want, history)
	}
	if history[0].Replicas != 3 || history[0].State != "suppressed" || history[0].Policy != "checkout-policy" {
		t.Fatalf("first history point = %#v, want suppressed 3", history[0])
	}
	if history[2].Replicas != 5 || history[2].State != "available" {
		t.Fatalf("last history point = %#v, want available 5", history[2])
	}
	if !reflect.DeepEqual(api.queries, []string{`skale_recommendation_recommended_replicas{namespace="payments",workload="checkout"}`}) {
		t.Fatalf("queries = %#v", api.queries)
	}
}

type fakeAPI struct {
	results map[string]RangeQueryResult
	errs    map[string]error
	queries []string
}

func (f *fakeAPI) QueryRange(_ context.Context, query string, _, _ time.Time, _ time.Duration) (RangeQueryResult, error) {
	f.queries = append(f.queries, query)
	if err, ok := f.errs[query]; ok {
		return RangeQueryResult{}, err
	}
	if result, ok := f.results[query]; ok {
		return result, nil
	}
	return RangeQueryResult{}, nil
}

func singleSeriesResult(first float64, second float64) RangeQueryResult {
	return RangeQueryResult{
		Series: []QuerySeries{
			querySeries("payments", "checkout", first, second),
		},
	}
}

func querySeries(namespace string, deployment string, first float64, second float64) QuerySeries {
	window := testWindow()
	return QuerySeries{
		Labels: map[string]string{
			"namespace":  namespace,
			"deployment": deployment,
		},
		Samples: []metrics.Sample{
			{Timestamp: window.Start.Add(30 * time.Second), Value: first},
			{Timestamp: window.Start.Add(60 * time.Second), Value: second},
		},
	}
}

func testWindow() metrics.Window {
	return metrics.Window{
		Start: time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC),
		End:   time.Date(2026, time.April, 2, 12, 10, 0, 0, time.UTC),
	}
}
