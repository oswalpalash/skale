package prometheus

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
)

var (
	ErrInvalidAdapterConfig = errors.New("invalid prometheus metrics adapter config")
	ErrSeriesMissing        = errors.New("prometheus query returned no series")
	ErrAmbiguousSeries      = errors.New("prometheus query returned multiple series")
	ErrMalformedSeries      = errors.New("prometheus series is malformed")
)

const defaultQueryStep = 30 * time.Second

// SignalError ties a query failure to the signal boundary that produced it.
type SignalError struct {
	Signal metrics.SignalName
	Query  string
	Err    error
}

func (e *SignalError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if strings.TrimSpace(e.Query) == "" {
		return fmt.Sprintf("%s: %v", e.Signal, e.Err)
	}
	return fmt.Sprintf("%s query %q failed: %v", e.Signal, e.Query, e.Err)
}

func (e *SignalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Adapter loads normalized metrics signals from Prometheus range queries.
type Adapter struct {
	API     API
	Queries Queries
	Step    time.Duration
}

var _ metrics.Provider = (*Adapter)(nil)
var _ metrics.WorkloadFetcher = (*Adapter)(nil)
var _ metrics.ClusterFetcher = (*Adapter)(nil)
var _ metrics.RecommendationHistoryProvider = (*Adapter)(nil)
var _ metrics.ForecastPredictionHistoryProvider = (*Adapter)(nil)

// LoadWindow fetches the supported workload and cluster signals for the window.
func (a Adapter) LoadWindow(ctx context.Context, target metrics.Target, window metrics.Window) (metrics.Snapshot, error) {
	if err := a.validateBase(window); err != nil {
		return metrics.Snapshot{}, err
	}
	if err := a.Queries.Validate(); err != nil {
		return metrics.Snapshot{}, err
	}

	workload, err := a.LoadWorkloadSignals(ctx, target, window)
	if err != nil {
		return metrics.Snapshot{}, err
	}

	cluster, err := a.LoadClusterSignals(ctx, target, window)
	if err != nil {
		return metrics.Snapshot{}, err
	}

	return workload.WithClusterSignals(window, cluster), nil
}

// LoadWorkloadSignals fetches workload-scoped telemetry such as demand, replicas, and readiness proxies.
func (a Adapter) LoadWorkloadSignals(ctx context.Context, target metrics.Target, window metrics.Window) (metrics.WorkloadSignals, error) {
	if err := a.validateBase(window); err != nil {
		return metrics.WorkloadSignals{}, err
	}
	if err := a.Queries.Validate(); err != nil {
		return metrics.WorkloadSignals{}, err
	}

	demand, err := a.loadSignal(ctx, target, window, metrics.SignalDemand)
	if err != nil {
		return metrics.WorkloadSignals{}, err
	}
	replicas, err := a.loadSignal(ctx, target, window, metrics.SignalReplicas)
	if err != nil {
		return metrics.WorkloadSignals{}, err
	}
	cpu, err := a.loadSignal(ctx, target, window, metrics.SignalCPU)
	if err != nil {
		return metrics.WorkloadSignals{}, err
	}
	memory, err := a.loadSignal(ctx, target, window, metrics.SignalMemory)
	if err != nil {
		return metrics.WorkloadSignals{}, err
	}
	latency, err := a.loadSignal(ctx, target, window, metrics.SignalLatency)
	if err != nil {
		return metrics.WorkloadSignals{}, err
	}
	errorRate, err := a.loadSignal(ctx, target, window, metrics.SignalErrors)
	if err != nil {
		return metrics.WorkloadSignals{}, err
	}
	warmup, err := a.loadSignal(ctx, target, window, metrics.SignalWarmup)
	if err != nil {
		return metrics.WorkloadSignals{}, err
	}

	return metrics.WorkloadSignals{
		Demand:   derefSeries(demand),
		Replicas: derefSeries(replicas),
		CPU:      cpu,
		Memory:   memory,
		Latency:  latency,
		Errors:   errorRate,
		Warmup:   warmup,
	}, nil
}

// LoadClusterSignals fetches cluster-scoped safety telemetry such as node headroom.
func (a Adapter) LoadClusterSignals(ctx context.Context, target metrics.Target, window metrics.Window) (metrics.ClusterSignals, error) {
	if err := a.validateBase(window); err != nil {
		return metrics.ClusterSignals{}, err
	}

	headroom, err := a.loadSignal(ctx, target, window, metrics.SignalNodeHeadroom)
	if err != nil {
		return metrics.ClusterSignals{}, err
	}

	return metrics.ClusterSignals{
		NodeHeadroom: headroom,
	}, nil
}

// LoadRecommendationHistory fetches controller-emitted recommendation samples for dashboard timeline overlays.
func (a Adapter) LoadRecommendationHistory(ctx context.Context, target metrics.Target, window metrics.Window) ([]metrics.RecommendationSample, error) {
	if err := a.validateBase(window); err != nil {
		return nil, err
	}

	query := fmt.Sprintf(
		`skale_recommendation_recommended_replicas{namespace=%q,workload=%q}`,
		target.Namespace,
		target.Name,
	)
	result, err := a.API.QueryRange(ctx, query, window.Start, window.End, a.queryStep())
	if err != nil {
		return nil, err
	}

	samples := make([]metrics.RecommendationSample, 0)
	for _, series := range result.Series {
		state := strings.TrimSpace(series.Labels["state"])
		policy := strings.TrimSpace(series.Labels["policy"])
		for _, sample := range series.Samples {
			samples = append(samples, metrics.RecommendationSample{
				Timestamp: sample.Timestamp.UTC(),
				Replicas:  sample.Value,
				State:     state,
				Policy:    policy,
			})
		}
	}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Timestamp.Equal(samples[j].Timestamp) {
			return samples[i].Policy < samples[j].Policy
		}
		return samples[i].Timestamp.Before(samples[j].Timestamp)
	})
	return samples, nil
}

// LoadForecastPredictionHistory fetches controller-emitted model prediction samples for dashboard timeline overlays.
func (a Adapter) LoadForecastPredictionHistory(ctx context.Context, target metrics.Target, window metrics.Window, horizon string) ([]metrics.ForecastPredictionSample, error) {
	if err := a.validateBase(window); err != nil {
		return nil, err
	}
	if strings.TrimSpace(horizon) == "" {
		horizon = "ready"
	}

	query := fmt.Sprintf(
		`skale_forecast_predicted_replicas{namespace=%q,workload=%q,horizon=%q}`,
		target.Namespace,
		target.Name,
		horizon,
	)
	result, err := a.API.QueryRange(ctx, query, window.Start, window.End, a.queryStep())
	if err != nil {
		return nil, err
	}

	samples := make([]metrics.ForecastPredictionSample, 0)
	for _, series := range result.Series {
		model := strings.TrimSpace(series.Labels["model"])
		policy := strings.TrimSpace(series.Labels["policy"])
		selected := strings.EqualFold(strings.TrimSpace(series.Labels["selected"]), "true")
		for _, sample := range series.Samples {
			samples = append(samples, metrics.ForecastPredictionSample{
				Timestamp: sample.Timestamp.UTC(),
				Model:     model,
				Horizon:   horizon,
				Replicas:  sample.Value,
				Policy:    policy,
				Selected:  selected,
			})
		}
	}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Timestamp.Equal(samples[j].Timestamp) {
			if samples[i].Model == samples[j].Model {
				return samples[i].Policy < samples[j].Policy
			}
			return samples[i].Model < samples[j].Model
		}
		return samples[i].Timestamp.Before(samples[j].Timestamp)
	})
	return samples, nil
}

func (a Adapter) validateBase(window metrics.Window) error {
	if a.API == nil {
		return fmt.Errorf("%w: api is required", ErrInvalidAdapterConfig)
	}
	if window.Start.IsZero() || window.End.IsZero() || !window.End.After(window.Start) {
		return fmt.Errorf("%w: invalid load window", ErrInvalidAdapterConfig)
	}
	return nil
}

func (a Adapter) loadSignal(
	ctx context.Context,
	target metrics.Target,
	window metrics.Window,
	name metrics.SignalName,
) (*metrics.SignalSeries, error) {
	query := a.Queries.queryFor(name)
	if strings.TrimSpace(query.Expr) == "" {
		if signalRequired(name, query) {
			return nil, &SignalError{
				Signal: name,
				Err:    ErrSeriesMissing,
			}
		}
		return nil, nil
	}

	rendered := query.Render(target)
	result, err := a.API.QueryRange(ctx, rendered, window.Start, window.End, a.queryStep())
	if err != nil {
		return nil, &SignalError{
			Signal: name,
			Query:  rendered,
			Err:    err,
		}
	}

	if len(result.Series) == 0 {
		if signalRequired(name, query) {
			return nil, &SignalError{
				Signal: name,
				Query:  rendered,
				Err:    ErrSeriesMissing,
			}
		}
		return nil, nil
	}

	if len(result.Series) != 1 {
		return nil, &SignalError{
			Signal: name,
			Query:  rendered,
			Err: fmt.Errorf(
				"%w: expected 1 series, got %d (%s)",
				ErrAmbiguousSeries,
				len(result.Series),
				strings.Join(labelSignatures(result.Series), "; "),
			),
		}
	}

	series, err := normalizeSeries(name, query.Unit, result.Series[0])
	if err != nil {
		return nil, &SignalError{
			Signal: name,
			Query:  rendered,
			Err:    err,
		}
	}

	return &series, nil
}

func (a Adapter) queryStep() time.Duration {
	if a.Step <= 0 {
		return defaultQueryStep
	}
	return a.Step
}

func normalizeSeries(name metrics.SignalName, unit string, series QuerySeries) (metrics.SignalSeries, error) {
	if len(series.Samples) == 0 {
		return metrics.SignalSeries{}, ErrSeriesMissing
	}

	normalized := metrics.SignalSeries{
		Name:                    name,
		Unit:                    unit,
		Samples:                 make([]metrics.Sample, 0, len(series.Samples)),
		ObservedLabelSignatures: []string{labelSignature(series.Labels)},
	}

	for index, sample := range series.Samples {
		if sample.Timestamp.IsZero() {
			return metrics.SignalSeries{}, fmt.Errorf("%w: sample %d has zero timestamp", ErrMalformedSeries, index)
		}
		if math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			return metrics.SignalSeries{}, fmt.Errorf("%w: sample %d has invalid value", ErrMalformedSeries, index)
		}
		if index > 0 && !sample.Timestamp.After(series.Samples[index-1].Timestamp) {
			return metrics.SignalSeries{}, fmt.Errorf("%w: sample timestamps must be strictly increasing", ErrMalformedSeries)
		}
		normalized.Samples = append(normalized.Samples, metrics.Sample{
			Timestamp: sample.Timestamp.UTC(),
			Value:     sample.Value,
		})
	}

	return normalized, nil
}

func labelSignatures(series []QuerySeries) []string {
	signatures := make([]string, 0, len(series))
	for _, entry := range series {
		signatures = append(signatures, labelSignature(entry.Labels))
	}
	sort.Strings(signatures)
	return signatures
}

func labelSignature(labels map[string]string) string {
	if len(labels) == 0 {
		return "<no-labels>"
	}

	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, labels[key]))
	}

	return strings.Join(parts, ",")
}

func derefSeries(series *metrics.SignalSeries) metrics.SignalSeries {
	if series == nil {
		return metrics.SignalSeries{}
	}
	return *series
}
