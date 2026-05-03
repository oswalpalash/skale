package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/explain"
	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/recommend"
	"github.com/oswalpalash/skale/internal/safety"
)

const (
	defaultControllerLookback        = 30 * time.Minute
	defaultControllerStep            = 30 * time.Second
	defaultForecastContextWindow     = 168 * time.Hour
	defaultForecastContextStep       = 5 * time.Minute
	defaultRecentContextWindow       = 2 * time.Hour
	defaultRecentContextStep         = 30 * time.Second
	defaultCapacityLookback          = 15 * time.Minute
	defaultMinimumCapacitySamples    = 3
	defaultSeasonalityMinCorrelation = 0.75
)

type evaluationStage string

const (
	evaluationStageDecision             evaluationStage = "decision"
	evaluationStageTelemetryUnavailable evaluationStage = "telemetry_unavailable"
	evaluationStageForecastUnavailable  evaluationStage = "forecast_unavailable"
)

// EvaluationPipeline orchestrates metrics, readiness, forecast, and recommendation modules for the live controller.
type EvaluationPipeline struct {
	MetricsProvider             metrics.Provider
	ReadinessEvaluator          metrics.Evaluator
	ForecastModel               forecast.Model
	RecommendEngine             recommend.Engine
	DependencyEvaluator         DependencyEvaluator
	HeadroomProvider            HeadroomProvider
	ReadinessExpectedResolution time.Duration
	ForecastSeasonalityOverride time.Duration
}

// LiveEvaluation is the controller-facing result of one live recommendation-only evaluation.
type LiveEvaluation struct {
	EvaluatedAt       time.Time
	Window            metrics.Window
	Workload          explain.WorkloadIdentity
	CurrentDemand     float64
	CurrentReplicas   int32
	TelemetryReport   metrics.ReadinessReport
	TelemetrySummary  explain.TelemetryReadinessSummary
	ForecastSummary   *explain.ForecastSummary
	ForecastMetrics   []ForecastMetric
	Recommendation    *explain.Decision
	SuppressionReason []explain.SuppressionReason
	Stage             evaluationStage
	Message           string
}

type ForecastMetric struct {
	Model        string
	Horizon      string
	Demand       float64
	Replicas     int32
	HasReplicas  bool
	Confidence   float64
	Reliability  string
	Selected     bool
	ForecastedAt time.Time
	TargetTime   time.Time
	Error        string
}

func (p EvaluationPipeline) Evaluate(
	ctx context.Context,
	policy *skalev1alpha1.PredictiveScalingPolicy,
	target ResolvedTarget,
	evaluatedAt time.Time,
	previous *safety.PreviousRecommendation,
) (LiveEvaluation, error) {
	provider := p.MetricsProvider
	if provider == nil {
		provider = metrics.NoopProvider{}
	}
	readinessEvaluator := p.ReadinessEvaluator
	if readinessEvaluator == nil {
		readinessEvaluator = metrics.DefaultEvaluator{}
	}
	forecastModel := p.ForecastModel
	if forecastModel == nil {
		forecastModel = forecast.AutoModel{}
	}
	recommendEngine := p.RecommendEngine
	if recommendEngine == nil {
		recommendEngine = recommend.DeterministicEngine{}
	}

	window := controllerForecastContextWindowSpec(policy.Spec, evaluatedAt)
	result := LiveEvaluation{
		EvaluatedAt: evaluatedAt.UTC(),
		Window:      window,
		Workload:    target.Identity,
	}

	metricsTarget := metrics.Target{
		Namespace: target.Identity.Namespace,
		Name:      target.Identity.Name,
	}
	snapshot, readinessSnapshot, err := loadTieredContext(ctx, provider, metricsTarget, policy.Spec, evaluatedAt)
	if err != nil {
		result.Stage = evaluationStageTelemetryUnavailable
		result.Message = fmt.Sprintf("telemetry inputs could not be loaded: %v", err)
		result.TelemetrySummary = explain.TelemetryReadinessSummary{
			CheckedAt: evaluatedAt.UTC(),
			State:     "unsupported",
			Message:   result.Message,
			Reasons:   []string{err.Error()},
		}
		return result, nil
	}
	result.Window = snapshot.Window

	readinessInput := metrics.ReadinessInput{
		EvaluatedAt: evaluatedAt,
		Snapshot:    readinessSnapshot,
		KnownWarmup: durationPtr(policy.Spec.Warmup.EstimatedReadyDuration.Duration),
		Options:     controllerReadinessOptions(policy.Spec, p.ReadinessExpectedResolution),
	}
	readinessReport, err := readinessEvaluator.Evaluate(readinessInput)
	if err != nil {
		return LiveEvaluation{}, fmt.Errorf("evaluate telemetry readiness: %w", err)
	}
	result.TelemetryReport = readinessReport
	result.TelemetrySummary = explain.TelemetrySummaryFromReadiness(readinessReport)
	result.CurrentDemand = latestSeriesValue(snapshot.Demand)
	result.CurrentReplicas = latestReplicaValue(snapshot.Replicas)

	if readinessReport.Level == metrics.ReadinessLevelUnsupported {
		result.Stage = evaluationStageTelemetryUnavailable
		result.Message = readinessReport.Summary
		result.SuppressionReason = []explain.SuppressionReason{{
			Code:     explain.ReasonTelemetryNotReady,
			Category: explain.SuppressionCategoryTelemetry,
			Severity: explain.SuppressionSeverityError,
			Message:  readinessReport.Summary,
		}}
		return result, nil
	}

	var dependencyHealth []safety.DependencyHealthStatus
	if len(policy.Spec.DependencyHealthChecks) > 0 {
		dependencyEvaluator := p.DependencyEvaluator
		if dependencyEvaluator == nil {
			dependencyEvaluator = NoopDependencyEvaluator{}
		}
		dependencyHealth, err = dependencyEvaluator.Evaluate(ctx, metrics.Target{
			Namespace: target.Identity.Namespace,
			Name:      target.Identity.Name,
		}, policy.Spec.DependencyHealthChecks, evaluatedAt)
		if err != nil {
			return LiveEvaluation{}, fmt.Errorf("evaluate dependency health: %w", err)
		}
	}

	var nodeHeadroom *safety.NodeHeadroomSignal
	if nodeHeadroomMode(policy.Spec.NodeHeadroomSanity) == safety.NodeHeadroomModeRequireForScaleUp && p.HeadroomProvider != nil {
		nodeHeadroom, err = p.HeadroomProvider.HeadroomFor(ctx, target, evaluatedAt)
		if err != nil {
			return LiveEvaluation{}, fmt.Errorf("evaluate node headroom: %w", err)
		}
	}

	demandPoints := signalSeriesToForecastPoints(snapshot.Demand)
	seasonality := controllerForecastSeasonality(policy.Spec, p.ForecastSeasonalityOverride, demandPoints)
	forecastStep := controllerRecentContextStep(policy.Spec)
	forecastInput := forecast.Input{
		Series:                demandPoints,
		EvaluatedAt:           evaluatedAt,
		Horizon:               policy.Spec.ForecastHorizon.Duration,
		Step:                  forecastStep,
		Seasonality:           seasonality.Period,
		SeasonalitySource:     seasonality.Source,
		SeasonalityConfidence: seasonality.Confidence,
	}
	forecastResult, err := forecastModel.Forecast(ctx, forecastInput)
	if err != nil {
		summary := explain.ForecastErrorSummary(forecastModel.Name(), evaluatedAt, err)
		result.ForecastSummary = &summary
		result.Stage = evaluationStageForecastUnavailable
		result.Message = err.Error()
		result.SuppressionReason = []explain.SuppressionReason{{
			Code:     explain.ReasonForecastUnavailable,
			Category: explain.SuppressionCategoryForecast,
			Severity: explain.SuppressionSeverityError,
			Message:  err.Error(),
		}}
		return result, nil
	}

	selectedPoint := selectForecastPoint(forecastResult.Points, evaluatedAt.Add(policy.Spec.Warmup.EstimatedReadyDuration.Duration))
	forecastSummary := explain.ForecastSummaryFromResult(forecastResult, selectedPoint, evaluatedAt)
	result.ForecastSummary = &forecastSummary

	capacityEstimate := controllerCapacityEstimate(snapshot, evaluatedAt, policy.Spec.TargetUtilization)
	result.ForecastMetrics = forecastMetricsFromResult(forecastResult, snapshot, policy.Spec, evaluatedAt, capacityEstimate)
	recommendation, err := recommendEngine.Recommend(recommend.Input{
		Workload:            target.Identity.Resource,
		WorkloadRef:         target.Identity,
		EvaluationTime:      evaluatedAt,
		ForecastMethod:      forecastResult.Model,
		ForecastedDemand:    selectedPoint.Value,
		ForecastTimestamp:   selectedPoint.Timestamp,
		ForecastSummary:     &forecastSummary,
		CurrentDemand:       result.CurrentDemand,
		CurrentReplicas:     result.CurrentReplicas,
		TargetUtilization:   policy.Spec.TargetUtilization,
		EstimatedWarmup:     policy.Spec.Warmup.EstimatedReadyDuration.Duration,
		TelemetrySummary:    &result.TelemetrySummary,
		CapacityEstimate:    capacityEstimate,
		ConfidenceScore:     forecastResult.Confidence,
		ConfidenceThreshold: policy.Spec.ConfidenceThreshold,
		MinReplicas:         policy.Spec.MinReplicas,
		MaxReplicas:         policy.Spec.MaxReplicas,
		MaxStepUp:           maxScaleChange(policy.Spec.ScaleUp),
		MaxStepDown:         maxScaleChange(policy.Spec.ScaleDown),
		CooldownWindow:      policy.Spec.CooldownWindow.Duration,
		LastRecommendation:  previous,
		NodeHeadroomMode:    nodeHeadroomMode(policy.Spec.NodeHeadroomSanity),
		NodeHeadroom:        nodeHeadroom,
		Telemetry:           telemetryStatusFromReadiness(readinessReport),
		BlackoutWindows:     mergePolicyWindows(policy.Spec.BlackoutWindows, policy.Spec.KnownEvents),
		DependencyHealth:    dependencyHealth,
	})
	if err != nil {
		return LiveEvaluation{}, fmt.Errorf("compute recommendation: %w", err)
	}

	result.Recommendation = decisionPtr(recommendation.Explanation)
	result.SuppressionReason = recommendation.SuppressionReasons
	result.Stage = evaluationStageDecision
	result.Message = recommendation.Explanation.Outcome.Message
	return result, nil
}

func controllerForecastContextWindowSpec(spec skalev1alpha1.PredictiveScalingPolicySpec, evaluatedAt time.Time) metrics.Window {
	return metrics.Window{
		Start: evaluatedAt.Add(-controllerForecastContextWindow(spec)),
		End:   evaluatedAt,
		Step:  controllerForecastContextStep(spec),
	}
}

func controllerRecentContextWindowSpec(spec skalev1alpha1.PredictiveScalingPolicySpec, evaluatedAt time.Time) metrics.Window {
	return metrics.Window{
		Start: evaluatedAt.Add(-controllerRecentContextWindow(spec)),
		End:   evaluatedAt,
		Step:  controllerRecentContextStep(spec),
	}
}

func controllerForecastContextWindow(spec skalev1alpha1.PredictiveScalingPolicySpec) time.Duration {
	if spec.ForecastContextWindow.Duration > 0 {
		return spec.ForecastContextWindow.Duration
	}
	if spec.ForecastSeasonality.Duration > 0 {
		if candidate := spec.ForecastSeasonality.Duration * 2; candidate > defaultControllerLookback {
			return candidate
		}
	}
	return defaultForecastContextWindow
}

func controllerForecastContextStep(spec skalev1alpha1.PredictiveScalingPolicySpec) time.Duration {
	if spec.ForecastContextStep.Duration > 0 {
		return spec.ForecastContextStep.Duration
	}
	return defaultForecastContextStep
}

func controllerRecentContextWindow(spec skalev1alpha1.PredictiveScalingPolicySpec) time.Duration {
	contextWindow := controllerForecastContextWindow(spec)
	recentWindow := spec.RecentContextWindow.Duration
	if recentWindow <= 0 {
		recentWindow = defaultRecentContextWindow
	}
	if recentWindow > contextWindow {
		return contextWindow
	}
	return recentWindow
}

func controllerRecentContextStep(spec skalev1alpha1.PredictiveScalingPolicySpec) time.Duration {
	recentStep := spec.RecentContextStep.Duration
	if recentStep <= 0 {
		recentStep = defaultRecentContextStep
	}
	contextStep := controllerForecastContextStep(spec)
	if contextStep > 0 && recentStep > contextStep {
		return contextStep
	}
	return recentStep
}

func controllerReadinessLookback(spec skalev1alpha1.PredictiveScalingPolicySpec) time.Duration {
	lookback := defaultControllerLookback
	if candidate := spec.ForecastHorizon.Duration * 6; candidate > lookback {
		lookback = candidate
	}
	if candidate := spec.Warmup.EstimatedReadyDuration.Duration * 4; candidate > lookback {
		lookback = candidate
	}
	return lookback
}

func controllerReadinessOptions(spec skalev1alpha1.PredictiveScalingPolicySpec, expectedResolution time.Duration) metrics.ReadinessOptions {
	options := metrics.DefaultReadinessOptions()
	options.MinimumLookback = controllerReadinessLookback(spec)
	if expectedResolution <= 0 {
		expectedResolution = defaultControllerStep
	}
	if recentStep := controllerRecentContextStep(spec); recentStep > expectedResolution {
		expectedResolution = recentStep
	}
	options.ExpectedResolution = expectedResolution
	return options
}

func loadTieredContext(
	ctx context.Context,
	provider metrics.Provider,
	target metrics.Target,
	spec skalev1alpha1.PredictiveScalingPolicySpec,
	evaluatedAt time.Time,
) (metrics.Snapshot, metrics.Snapshot, error) {
	baseWindow := controllerForecastContextWindowSpec(spec, evaluatedAt)
	recentWindow := controllerRecentContextWindowSpec(spec, evaluatedAt)
	baseSnapshot, err := provider.LoadWindow(ctx, target, baseWindow)
	if err != nil {
		return metrics.Snapshot{}, metrics.Snapshot{}, err
	}

	if sameWindow(baseWindow, recentWindow) {
		return baseSnapshot, baseSnapshot, nil
	}

	recentSnapshot, err := provider.LoadWindow(ctx, target, recentWindow)
	if err != nil {
		return metrics.Snapshot{}, metrics.Snapshot{}, err
	}

	return mergeSnapshots(baseSnapshot, recentSnapshot), recentSnapshot, nil
}

func sameWindow(a metrics.Window, b metrics.Window) bool {
	return a.Start.Equal(b.Start) && a.End.Equal(b.End) && a.Step == b.Step
}

func mergeSnapshots(base metrics.Snapshot, recent metrics.Snapshot) metrics.Snapshot {
	return metrics.Snapshot{
		Window: metrics.Window{
			Start: base.Window.Start,
			End:   recent.Window.End,
			Step:  recent.Window.Step,
		},
		Demand:       mergeSeries(base.Demand, recent.Demand),
		Replicas:     mergeSeries(base.Replicas, recent.Replicas),
		CPU:          mergeOptionalSeries(base.CPU, recent.CPU),
		Memory:       mergeOptionalSeries(base.Memory, recent.Memory),
		Latency:      mergeOptionalSeries(base.Latency, recent.Latency),
		Errors:       mergeOptionalSeries(base.Errors, recent.Errors),
		Warmup:       mergeOptionalSeries(base.Warmup, recent.Warmup),
		NodeHeadroom: mergeOptionalSeries(base.NodeHeadroom, recent.NodeHeadroom),
	}
}

func mergeOptionalSeries(base *metrics.SignalSeries, recent *metrics.SignalSeries) *metrics.SignalSeries {
	switch {
	case base == nil && recent == nil:
		return nil
	case base == nil:
		merged := mergeSeries(metrics.SignalSeries{Name: recent.Name, Unit: recent.Unit}, *recent)
		return &merged
	case recent == nil:
		merged := mergeSeries(*base, metrics.SignalSeries{Name: base.Name, Unit: base.Unit})
		return &merged
	default:
		merged := mergeSeries(*base, *recent)
		return &merged
	}
}

func mergeSeries(base metrics.SignalSeries, recent metrics.SignalSeries) metrics.SignalSeries {
	out := metrics.SignalSeries{
		Name:                    base.Name,
		Unit:                    base.Unit,
		ObservedLabelSignatures: mergeStrings(base.ObservedLabelSignatures, recent.ObservedLabelSignatures),
	}
	if out.Name == "" {
		out.Name = recent.Name
	}
	if out.Unit == "" {
		out.Unit = recent.Unit
	}

	samplesByTimestamp := make(map[time.Time]metrics.Sample, len(base.Samples)+len(recent.Samples))
	for _, sample := range base.Samples {
		samplesByTimestamp[sample.Timestamp.UTC()] = metrics.Sample{
			Timestamp: sample.Timestamp.UTC(),
			Value:     sample.Value,
		}
	}
	for _, sample := range recent.Samples {
		samplesByTimestamp[sample.Timestamp.UTC()] = metrics.Sample{
			Timestamp: sample.Timestamp.UTC(),
			Value:     sample.Value,
		}
	}

	out.Samples = make([]metrics.Sample, 0, len(samplesByTimestamp))
	for _, sample := range samplesByTimestamp {
		out.Samples = append(out.Samples, sample)
	}
	sort.Slice(out.Samples, func(i, j int) bool {
		return out.Samples[i].Timestamp.Before(out.Samples[j].Timestamp)
	})
	return out
}

func mergeStrings(left []string, right []string) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	out := make([]string, 0, len(left)+len(right))
	for _, value := range append(append([]string(nil), left...), right...) {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func forecastMetricsFromResult(result forecast.Result, snapshot metrics.Snapshot, spec skalev1alpha1.PredictiveScalingPolicySpec, evaluatedAt time.Time, capacityEstimate *recommend.CapacityEstimate) []ForecastMetric {
	candidates := forecastMetricCandidates(result)
	out := make([]ForecastMetric, 0, len(candidates))
	targetTime := evaluatedAt.Add(spec.Warmup.EstimatedReadyDuration.Duration)
	capacity := 0.0
	if capacityEstimate != nil && capacityEstimate.Estimated && capacityEstimate.PerReplicaCapacity > 0 {
		capacity = capacityEstimate.PerReplicaCapacity
	} else {
		capacity = dashboardPerReplicaCapacity(snapshot, spec, evaluatedAt)
	}
	currentReplicasValue, replicasOK := valueAtOrBefore(snapshot.Replicas.Samples, evaluatedAt)
	currentReplicas := int32(0)
	if replicasOK && currentReplicasValue >= 1 {
		currentReplicas = int32(currentReplicasValue)
	}
	for _, candidate := range candidates {
		metric := ForecastMetric{
			Model:        candidate.Model,
			Horizon:      "ready",
			Confidence:   candidate.Confidence,
			Reliability:  string(candidate.Reliability),
			Selected:     candidate.Selected,
			ForecastedAt: evaluatedAt.UTC(),
			TargetTime:   targetTime.UTC(),
			Error:        candidate.Error,
		}
		point := selectForecastPoint(candidate.Points, targetTime)
		metric.Demand = point.Value
		if point.Timestamp.IsZero() {
			metric.TargetTime = targetTime.UTC()
		} else {
			metric.TargetTime = point.Timestamp.UTC()
		}
		if capacity > 0 && currentReplicas > 0 && candidate.Error == "" && len(candidate.Points) > 0 {
			replicas := requiredDashboardReplicas(point.Value, capacity)
			replicas = boundDashboardReplicas(replicas, spec.MinReplicas, spec.MaxReplicas)
			replicas = stepBoundDashboardReplicas(replicas, currentReplicas, spec)
			metric.Replicas = replicas
			metric.HasReplicas = true
		}
		out = append(out, metric)
	}
	return out
}

func forecastMetricCandidates(result forecast.Result) []forecast.CandidateResult {
	if len(result.Candidates) > 0 {
		return result.Candidates
	}
	return []forecast.CandidateResult{{
		Model:       result.Model,
		Points:      result.Points,
		Confidence:  result.Confidence,
		Reliability: result.Reliability,
		Selected:    true,
	}}
}

type resolvedSeasonality struct {
	Period     time.Duration
	Source     forecast.SeasonalitySource
	Confidence float64
}

func controllerForecastSeasonality(spec skalev1alpha1.PredictiveScalingPolicySpec, override time.Duration, series []forecast.Point) resolvedSeasonality {
	if override > 0 {
		return resolvedSeasonality{Period: override, Source: forecast.SeasonalitySourceConfigured, Confidence: 1}
	}
	if spec.ForecastSeasonality.Duration > 0 {
		return resolvedSeasonality{Period: spec.ForecastSeasonality.Duration, Source: forecast.SeasonalitySourceConfigured, Confidence: 1}
	}
	detection := forecast.DetectSeasonality(series, forecast.SeasonalityDetectionOptions{
		MinPeriod:      minSeasonalityPeriod(series),
		MaxPeriod:      maxSeasonalityPeriod(series),
		MinCycles:      3,
		MinCorrelation: defaultSeasonalityMinCorrelation,
	})
	if detection.Detected {
		return resolvedSeasonality{Period: detection.Period, Source: forecast.SeasonalitySourceDetected, Confidence: detection.Confidence}
	}
	return resolvedSeasonality{Source: forecast.SeasonalitySourceNone}
}

func minSeasonalityPeriod(series []forecast.Point) time.Duration {
	step := inferForecastStep(series)
	if step <= 0 {
		return 0
	}
	return 2 * step
}

func maxSeasonalityPeriod(series []forecast.Point) time.Duration {
	step := inferForecastStep(series)
	if step <= 0 || len(series) < 6 {
		return 0
	}
	return time.Duration(len(series)/3) * step
}

func inferForecastStep(series []forecast.Point) time.Duration {
	if len(series) < 2 {
		return 0
	}
	deltas := make([]int64, 0, len(series)-1)
	for index := 1; index < len(series); index++ {
		delta := series[index].Timestamp.Sub(series[index-1].Timestamp)
		if delta <= 0 {
			return 0
		}
		deltas = append(deltas, int64(delta))
	}
	sort.Slice(deltas, func(i, j int) bool {
		return deltas[i] < deltas[j]
	})
	return time.Duration(deltas[len(deltas)/2])
}

func controllerCapacityEstimate(snapshot metrics.Snapshot, evaluatedAt time.Time, targetUtilization float64) *recommend.CapacityEstimate {
	return capacityEstimateFromSeries(snapshot.Demand, snapshot.Replicas, evaluatedAt, defaultCapacityLookback, defaultMinimumCapacitySamples, targetUtilization)
}

func capacityEstimateFromSeries(demandSeries metrics.SignalSeries, replicaSeries metrics.SignalSeries, evaluatedAt time.Time, lookback time.Duration, minimumSamples int, targetUtilization float64) *recommend.CapacityEstimate {
	if lookback <= 0 {
		lookback = defaultCapacityLookback
	}
	if minimumSamples < 1 {
		minimumSamples = defaultMinimumCapacitySamples
	}
	estimate := &recommend.CapacityEstimate{
		WindowStart: evaluatedAt.Add(-lookback).UTC(),
		WindowEnd:   evaluatedAt.UTC(),
	}
	if targetUtilization <= 0 || targetUtilization > 1 {
		return estimate
	}

	capacities := make([]float64, 0, minimumSamples)
	for _, demandSample := range demandSeries.Samples {
		if demandSample.Timestamp.Before(estimate.WindowStart) || demandSample.Timestamp.After(estimate.WindowEnd) {
			continue
		}
		replicas, ok := valueAtOrBefore(replicaSeries.Samples, demandSample.Timestamp)
		if !ok || replicas < 1 || demandSample.Value <= 0 {
			continue
		}
		capacities = append(capacities, demandSample.Value/(replicas*targetUtilization))
	}
	estimate.SampleCount = len(capacities)
	if len(capacities) < minimumSamples {
		return estimate
	}
	capacity := medianFloat(capacities)
	if capacity <= 0 {
		return estimate
	}
	estimate.Estimated = true
	estimate.PerReplicaCapacity = capacity
	return estimate
}

func valueAtOrBefore(samples []metrics.Sample, at time.Time) (float64, bool) {
	if len(samples) == 0 {
		return 0, false
	}
	index := sort.Search(len(samples), func(i int) bool {
		return !samples[i].Timestamp.Before(at)
	})
	switch {
	case index < len(samples) && samples[index].Timestamp.Equal(at):
		return samples[index].Value, true
	case index == 0:
		return 0, false
	default:
		return samples[index-1].Value, true
	}
}

func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func signalSeriesToForecastPoints(series metrics.SignalSeries) []forecast.Point {
	out := make([]forecast.Point, 0, len(series.Samples))
	for _, sample := range series.Samples {
		out = append(out, forecast.Point{
			Timestamp: sample.Timestamp,
			Value:     sample.Value,
		})
	}
	return out
}

func selectForecastPoint(points []forecast.Point, target time.Time) forecast.Point {
	if len(points) == 0 {
		return forecast.Point{}
	}
	for _, point := range points {
		if point.Timestamp.Equal(target) || point.Timestamp.After(target) {
			return point
		}
	}
	return points[len(points)-1]
}

func latestSeriesValue(series metrics.SignalSeries) float64 {
	if len(series.Samples) == 0 {
		return 0
	}
	return series.Samples[len(series.Samples)-1].Value
}

func latestReplicaValue(series metrics.SignalSeries) int32 {
	value := latestSeriesValue(series)
	if value <= 0 {
		return 0
	}
	return int32(value)
}

func maxScaleChange(policy *skalev1alpha1.ScaleStepPolicy) *int32 {
	if policy == nil {
		return nil
	}
	value := policy.MaxReplicasChange
	return &value
}

func nodeHeadroomMode(mode skalev1alpha1.NodeHeadroomSanityMode) safety.NodeHeadroomMode {
	switch mode {
	case skalev1alpha1.NodeHeadroomSanityDisabled:
		return safety.NodeHeadroomModeDisabled
	default:
		return safety.NodeHeadroomModeRequireForScaleUp
	}
}

func telemetryStatusFromReadiness(report metrics.ReadinessReport) *safety.TelemetryStatus {
	level := safety.TelemetryLevelReady
	switch report.Level {
	case metrics.ReadinessLevelSupported:
		level = safety.TelemetryLevelReady
	case metrics.ReadinessLevelDegraded:
		level = safety.TelemetryLevelDegraded
	default:
		level = safety.TelemetryLevelUnsupported
	}

	message := strings.TrimSpace(report.Summary)
	if message == "" && len(report.Reasons) > 0 {
		message = strings.Join(report.Reasons, "; ")
	}

	return &safety.TelemetryStatus{
		Level:   level,
		Message: message,
		Reasons: append([]string(nil), report.Reasons...),
	}
}

func durationPtr(value time.Duration) *time.Duration {
	return &value
}

func decisionPtr(value explain.Decision) *explain.Decision {
	return &value
}
