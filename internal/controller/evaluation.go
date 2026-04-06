package controller

import (
	"context"
	"fmt"
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
	defaultControllerLookback = 30 * time.Minute
	defaultControllerStep     = 30 * time.Second
	defaultTargetUtilization  = 0.8
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
	Recommendation    *explain.Decision
	SuppressionReason []explain.SuppressionReason
	Stage             evaluationStage
	Message           string
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

	window := metrics.Window{
		Start: evaluatedAt.Add(-controllerLookback(policy.Spec)),
		End:   evaluatedAt,
	}
	result := LiveEvaluation{
		EvaluatedAt: evaluatedAt.UTC(),
		Window:      window,
		Workload:    target.Identity,
	}

	snapshot, err := provider.LoadWindow(ctx, metrics.Target{
		Namespace: target.Identity.Namespace,
		Name:      target.Identity.Name,
	}, window)
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

	readinessInput := metrics.ReadinessInput{
		EvaluatedAt: evaluatedAt,
		Snapshot:    snapshot,
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

	forecastInput := forecast.Input{
		Series:      signalSeriesToForecastPoints(snapshot.Demand),
		EvaluatedAt: evaluatedAt,
		Horizon:     policy.Spec.ForecastHorizon.Duration,
		Step:        0,
		Seasonality: controllerForecastSeasonality(policy.Spec, p.ForecastSeasonalityOverride),
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
		TargetUtilization:   defaultTargetUtilization,
		EstimatedWarmup:     policy.Spec.Warmup.EstimatedReadyDuration.Duration,
		TelemetrySummary:    &result.TelemetrySummary,
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

func controllerLookback(spec skalev1alpha1.PredictiveScalingPolicySpec) time.Duration {
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
	options.MinimumLookback = controllerLookback(spec)
	if expectedResolution <= 0 {
		expectedResolution = defaultControllerStep
	}
	options.ExpectedResolution = expectedResolution
	return options
}

func controllerForecastSeasonality(spec skalev1alpha1.PredictiveScalingPolicySpec, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	if spec.ForecastHorizon.Duration <= 0 {
		return 0
	}
	// v1 keeps the live controller simple: it reuses one forecast-horizon-length season
	// instead of pretending to infer richer periodicity from CRD configuration that does not exist yet.
	return spec.ForecastHorizon.Duration
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
