package replay

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/oswalpalash/skale/internal/explain"
	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/recommend"
	"github.com/oswalpalash/skale/internal/safety"
)

var (
	ErrInvalidSpec     = errors.New("invalid replay spec")
	ErrMetricsRequired = errors.New("replay metrics provider is required")
)

type Status string

const (
	StatusComplete    Status = "complete"
	StatusUnsupported Status = "unsupported"
)

type BaselineMode string

const (
	BaselineModeObservedReplicas BaselineMode = "observedReplicas"
)

type KnownEvent struct {
	Name  string
	Start time.Time
	End   time.Time
	Note  string
}

type HeadroomObservation struct {
	ObservedAt time.Time
	Signal     safety.NodeHeadroomSignal
}

// Policy describes the replayed recommendation behavior for one supported workload.
type Policy struct {
	Workload string

	ForecastHorizon     time.Duration
	ForecastSeasonality time.Duration
	Warmup              time.Duration

	TargetUtilization   float64
	ConfidenceThreshold float64

	MinReplicas int32
	MaxReplicas int32
	MaxStepUp   *int32
	MaxStepDown *int32

	CooldownWindow   time.Duration
	BlackoutWindows  []safety.BlackoutWindow
	KnownEvents      []KnownEvent
	DependencyHealth []safety.DependencyHealthStatus
	NodeHeadroomMode safety.NodeHeadroomMode
}

// Options control replay-only assumptions that are intentionally explicit.
type Options struct {
	BaselineMode           BaselineMode
	CapacityLookback       time.Duration
	MinimumCapacitySamples int
	ReadinessOptions       metrics.ReadinessOptions
	HeadroomTimeline       []HeadroomObservation
}

// Spec identifies one historical replay run.
type Spec struct {
	Target   metrics.Target
	Window   metrics.Window
	Step     time.Duration
	Lookback time.Duration
	Policy   Policy
	Options  Options
}

type TargetRef struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
}

type WindowSummary struct {
	Start time.Time `json:"start,omitempty"`
	End   time.Time `json:"end,omitempty"`
}

type PolicySummary struct {
	Workload                   string  `json:"workload,omitempty"`
	ForecastHorizonSeconds     int64   `json:"forecastHorizonSeconds,omitempty"`
	ForecastSeasonalitySeconds int64   `json:"forecastSeasonalitySeconds,omitempty"`
	WarmupSeconds              int64   `json:"warmupSeconds,omitempty"`
	TargetUtilization          float64 `json:"targetUtilization,omitempty"`
	ConfidenceThreshold        float64 `json:"confidenceThreshold,omitempty"`
	MinReplicas                int32   `json:"minReplicas,omitempty"`
	MaxReplicas                int32   `json:"maxReplicas,omitempty"`
	MaxStepUp                  *int32  `json:"maxStepUp,omitempty"`
	MaxStepDown                *int32  `json:"maxStepDown,omitempty"`
	CooldownSeconds            int64   `json:"cooldownSeconds,omitempty"`
	NodeHeadroomMode           string  `json:"nodeHeadroomMode,omitempty"`
}

type Summary struct {
	StartReplicas              int32   `json:"startReplicas,omitempty"`
	EndReplicas                int32   `json:"endReplicas,omitempty"`
	MinReplicas                int32   `json:"minReplicas,omitempty"`
	MaxReplicas                int32   `json:"maxReplicas,omitempty"`
	MeanReplicas               float64 `json:"meanReplicas,omitempty"`
	ScaleUpEvents              int     `json:"scaleUpEvents,omitempty"`
	ScaleDownEvents            int     `json:"scaleDownEvents,omitempty"`
	OverloadMinutesProxy       float64 `json:"overloadMinutesProxy,omitempty"`
	ExcessHeadroomMinutesProxy float64 `json:"excessHeadroomMinutesProxy,omitempty"`
	ScoredMinutes              float64 `json:"scoredMinutes,omitempty"`
	UnscoredMinutes            float64 `json:"unscoredMinutes,omitempty"`
}

type BaselineSummary struct {
	Mode SummaryMode `json:"mode,omitempty"`
	Summary
}

type SummaryMode string

const (
	SummaryModeObservedReplicas SummaryMode = "observedReplicas"
	SummaryModeSimulatedReplay  SummaryMode = "simulatedReplay"
)

type ReplaySummary struct {
	Mode SummaryMode `json:"mode,omitempty"`
	Summary

	EvaluationCount          int            `json:"evaluationCount,omitempty"`
	AvailableCount           int            `json:"availableCount,omitempty"`
	SuppressedCount          int            `json:"suppressedCount,omitempty"`
	UnavailableCount         int            `json:"unavailableCount,omitempty"`
	RecommendationEventCount int            `json:"recommendationEventCount,omitempty"`
	SuppressionReasonCounts  map[string]int `json:"suppressionReasonCounts,omitempty"`
	ForecastModelCounts      map[string]int `json:"forecastModelCounts,omitempty"`
	ReliabilityCounts        map[string]int `json:"reliabilityCounts,omitempty"`
}

type ForecastEvaluation = explain.ForecastSummary

type Evaluation struct {
	EvaluatedAt           time.Time                         `json:"evaluatedAt,omitempty"`
	HistoryWindow         WindowSummary                     `json:"historyWindow,omitempty"`
	CurrentDemand         float64                           `json:"currentDemand,omitempty"`
	BaselineReplicas      int32                             `json:"baselineReplicas,omitempty"`
	SimulatedReplicas     int32                             `json:"simulatedReplicas,omitempty"`
	CapacityEstimated     bool                              `json:"capacityEstimated,omitempty"`
	CapacityPerReplica    float64                           `json:"capacityPerReplica,omitempty"`
	RequiredReplicasProxy *int32                            `json:"requiredReplicasProxy,omitempty"`
	BaselineOverloaded    bool                              `json:"baselineOverloaded,omitempty"`
	ReplayOverloaded      bool                              `json:"replayOverloaded,omitempty"`
	BaselineExcess        bool                              `json:"baselineExcess,omitempty"`
	ReplayExcess          bool                              `json:"replayExcess,omitempty"`
	Forecast              ForecastEvaluation                `json:"forecast"`
	Telemetry             explain.TelemetryReadinessSummary `json:"telemetry"`
	Decision              *explain.Decision                 `json:"decision,omitempty"`
	State                 string                            `json:"state,omitempty"`
	SuppressionReasons    []explain.SuppressionReason       `json:"suppressionReasons,omitempty"`
	ActivationTime        *time.Time                        `json:"activationTime,omitempty"`
	Caveats               []string                          `json:"caveats,omitempty"`
	Message               string                            `json:"message,omitempty"`
}

type RecommendationEvent = explain.ReplayEventExplanation

// Result is the replay output shared with CLI and report writers.
type Result struct {
	Status               Status                            `json:"status,omitempty"`
	GeneratedAt          time.Time                         `json:"generatedAt,omitempty"`
	Target               TargetRef                         `json:"target"`
	Window               WindowSummary                     `json:"window"`
	StepSeconds          int64                             `json:"stepSeconds,omitempty"`
	LookbackSeconds      int64                             `json:"lookbackSeconds,omitempty"`
	Policy               PolicySummary                     `json:"policy"`
	TelemetryReadiness   explain.TelemetryReadinessSummary `json:"telemetryReadiness"`
	Baseline             BaselineSummary                   `json:"baseline"`
	Replay               ReplaySummary                     `json:"replay"`
	RecommendationEvents []RecommendationEvent             `json:"recommendationEvents,omitempty"`
	Evaluations          []Evaluation                      `json:"evaluations,omitempty"`
	Caveats              []string                          `json:"caveats,omitempty"`
	ConfidenceNotes      []string                          `json:"confidenceNotes,omitempty"`
	UnsupportedReasons   []string                          `json:"unsupportedReasons,omitempty"`
}

// Engine coordinates metrics, forecast, recommendation, safety, and explanation stages.
type Engine struct {
	Metrics   metrics.Provider
	Forecast  forecast.Model
	Recommend recommend.Engine
	Safety    safety.Evaluator
	Explain   explain.Builder
	Readiness metrics.Evaluator
}

type observation struct {
	At       time.Time
	Demand   float64
	Replicas int32
}

type scheduledActivation struct {
	At       time.Time
	Replicas int32
}

// Run executes a fixed-step historical replay for one supported workload.
func (e Engine) Run(ctx context.Context, spec Spec) (Result, error) {
	resolved, err := spec.withDefaults()
	if err != nil {
		return Result{}, err
	}
	if e.Metrics == nil {
		return Result{}, ErrMetricsRequired
	}

	fetchWindow := metrics.Window{
		Start: resolved.Window.Start.Add(-resolved.Lookback),
		End:   resolved.Window.End,
	}
	snapshot, err := e.Metrics.LoadWindow(ctx, resolved.Target, fetchWindow)
	if err != nil {
		return Result{}, err
	}

	result := newResult(resolved)
	workloadRef := explain.WorkloadIdentity{
		Namespace: resolved.Target.Namespace,
		Name:      resolved.Target.Name,
		Resource:  resolved.Policy.Workload,
	}
	if workloadRef.Name == "" && workloadRef.Resource != "" {
		workloadRef = explain.WorkloadIdentityFromString(workloadRef.Resource)
	}
	result.Caveats = append(result.Caveats,
		"Replay uses observed replica behavior as the baseline; it does not reconstruct HPA internals.",
		fmt.Sprintf("Warmup lag is modeled as a fixed %s delay before recommended replicas become useful.", resolved.Policy.Warmup),
		"Scheduling delay beyond the configured warmup lag is not modeled in v1 replay.",
		"Overload and excess headroom are proxy minutes derived from observed demand and estimated replica capacity, not SLA or latency outcomes.",
		"Observed historical demand may understate unmet demand during overloaded periods, so replay can be optimistic.",
	)
	if resolved.Options.BaselineMode == BaselineModeObservedReplicas {
		result.Caveats = append(result.Caveats, "Baseline behavior is reconstructed directly from the observed replica series.")
	}
	if resolved.Policy.NodeHeadroomMode == "" || resolved.Policy.NodeHeadroomMode == safety.NodeHeadroomModeDisabled {
		result.Caveats = append(result.Caveats, "Node headroom safety is disabled for this replay run.")
	}

	readinessEvaluator := e.Readiness
	if readinessEvaluator == nil {
		readinessEvaluator = metrics.DefaultEvaluator{}
	}

	fullReadiness, err := readinessEvaluator.Evaluate(metrics.ReadinessInput{
		EvaluatedAt: resolved.Window.End,
		Snapshot:    snapshot,
		KnownWarmup: durationPtr(resolved.Policy.Warmup),
		Options:     resolved.Options.ReadinessOptions,
	})
	if err != nil {
		return Result{}, err
	}
	result.TelemetryReadiness = explain.TelemetrySummaryFromReadiness(fullReadiness)
	if fullReadiness.Level == metrics.ReadinessLevelDegraded {
		result.Caveats = append(result.Caveats, fullReadiness.Summary)
	}

	grid := buildStepGrid(fetchWindow.Start, resolved.Window.End, resolved.Step)
	if len(grid) == 0 {
		return Result{}, fmt.Errorf("%w: replay grid is empty", ErrInvalidSpec)
	}

	observed, err := buildObservedGrid(snapshot, grid)
	if err != nil {
		return Result{}, err
	}

	baselineSummary, err := buildBaselineSummary(observed, resolved.Window)
	if err != nil {
		result.Status = StatusUnsupported
		result.UnsupportedReasons = append(result.UnsupportedReasons, err.Error())
		result.Caveats = dedupeStrings(result.Caveats)
		return result, nil
	}
	result.Baseline = baselineSummary

	if fullReadiness.Level == metrics.ReadinessLevelUnsupported {
		result.Status = StatusUnsupported
		result.UnsupportedReasons = append(result.UnsupportedReasons, fullReadiness.BlockingReasons...)
		result.Caveats = dedupeStrings(result.Caveats)
		result.ConfidenceNotes = buildConfidenceNotes(result.TelemetryReadiness, nil, nil)
		return result, nil
	}

	forecastModel := e.Forecast
	if forecastModel == nil {
		forecastModel = forecast.AutoModel{}
	}

	recommendEngine := e.Recommend
	if recommendEngine == nil {
		recommendEngine = recommend.DeterministicEngine{
			Safety:  e.Safety,
			Explain: e.Explain,
		}
	}

	replayObs := make([]observation, 0, len(observed))
	evaluations := make([]Evaluation, 0, len(observed))
	events := make([]RecommendationEvent, 0)
	suppressionCounts := map[string]int{}
	forecastCounts := map[string]int{}
	reliabilityCounts := map[string]int{}
	advisoryCounts := map[string]int{}

	currentSimulated := baselineSummary.StartReplicas
	minSimulated := currentSimulated
	maxSimulated := currentSimulated
	var sumSimulated float64
	replayScaleUps := 0
	replayScaleDowns := 0
	var baselineOverloadMinutes float64
	var baselineExcessMinutes float64
	var replayOverloadMinutes float64
	var replayExcessMinutes float64
	var scoredMinutes float64
	var unscoredMinutes float64
	lastRecommended := &safety.PreviousRecommendation{
		RecommendedReplicas: currentSimulated,
	}
	var pending []scheduledActivation

	for index, stepObs := range observed {
		if stepObs.At.Before(resolved.Window.Start) {
			continue
		}
		if stepObs.At.After(resolved.Window.End) {
			break
		}

		currentSimulated, replayScaleUps, replayScaleDowns = applyScheduledActivations(currentSimulated, pending, stepObs.At, replayScaleUps, replayScaleDowns)
		pending = pendingAfter(pending, stepObs.At)

		if currentSimulated < minSimulated {
			minSimulated = currentSimulated
		}
		if currentSimulated > maxSimulated {
			maxSimulated = currentSimulated
		}
		sumSimulated += float64(currentSimulated)
		replayObs = append(replayObs, observation{
			At:       stepObs.At,
			Demand:   stepObs.Demand,
			Replicas: currentSimulated,
		})

		evaluation := Evaluation{
			EvaluatedAt:       stepObs.At,
			HistoryWindow:     WindowSummary{Start: stepObs.At.Add(-resolved.Lookback), End: stepObs.At},
			CurrentDemand:     stepObs.Demand,
			BaselineReplicas:  stepObs.Replicas,
			SimulatedReplicas: currentSimulated,
			State:             string(recommend.StateUnavailable),
		}

		stepWindow := metrics.Window{
			Start: stepObs.At.Add(-resolved.Lookback),
			End:   stepObs.At,
		}
		stepSnapshot := sliceSnapshot(snapshot, stepWindow)
		stepReadiness, err := readinessEvaluator.Evaluate(metrics.ReadinessInput{
			EvaluatedAt: stepObs.At,
			Snapshot:    stepSnapshot,
			KnownWarmup: durationPtr(resolved.Policy.Warmup),
			Options:     resolved.Options.ReadinessOptions,
		})
		if err != nil {
			return Result{}, err
		}
		stepTelemetrySummary := explain.TelemetrySummaryFromReadiness(stepReadiness)
		evaluation.Telemetry = stepTelemetrySummary

		forecastInput := forecast.Input{
			Series:      signalSeriesToForecastPoints(stepSnapshot.Demand),
			EvaluatedAt: stepObs.At,
			Horizon:     resolved.Policy.ForecastHorizon,
			Step:        resolved.Step,
			Seasonality: resolved.Policy.ForecastSeasonality,
		}
		forecastResult, forecastErr := forecastModel.Forecast(ctx, forecastInput)
		if forecastErr != nil {
			evaluation.Forecast = explain.ForecastErrorSummary(forecastModel.Name(), stepObs.At, forecastErr)
			evaluation.SuppressionReasons = []explain.SuppressionReason{{
				Code:     explain.ReasonForecastUnavailable,
				Category: explain.SuppressionCategoryForecast,
				Severity: explain.SuppressionSeverityError,
				Message:  forecastErr.Error(),
			}}
			evaluation.Message = forecastErr.Error()
			evaluation.Caveats = append(evaluation.Caveats, "Forecast could not produce a usable historical demand estimate at this step.")
			suppressionCounts[explain.ReasonForecastUnavailable]++
			evaluations = append(evaluations, evaluation)
			continue
		}

		selectedPoint := selectForecastPoint(forecastResult.Points, stepObs.At.Add(resolved.Policy.Warmup))
		forecastSummary := explain.ForecastSummaryFromResult(forecastResult, selectedPoint, stepObs.At)
		evaluation.Forecast = forecastSummary
		forecastCounts[forecastResult.Model]++
		reliabilityCounts[string(forecastResult.Reliability)]++
		for _, advisory := range forecastResult.Advisories {
			advisoryCounts[advisory.Code]++
		}

		recommendation, err := recommendEngine.Recommend(recommend.Input{
			Workload:            resolved.Policy.Workload,
			WorkloadRef:         workloadRef,
			EvaluationTime:      stepObs.At,
			ForecastMethod:      forecastResult.Model,
			ForecastedDemand:    selectedPoint.Value,
			ForecastTimestamp:   selectedPoint.Timestamp,
			ForecastSummary:     &forecastSummary,
			CurrentDemand:       stepObs.Demand,
			CurrentReplicas:     currentSimulated,
			TargetUtilization:   resolved.Policy.TargetUtilization,
			EstimatedWarmup:     resolved.Policy.Warmup,
			TelemetrySummary:    &stepTelemetrySummary,
			ConfidenceScore:     forecastResult.Confidence,
			ConfidenceThreshold: resolved.Policy.ConfidenceThreshold,
			MinReplicas:         resolved.Policy.MinReplicas,
			MaxReplicas:         resolved.Policy.MaxReplicas,
			MaxStepUp:           resolved.Policy.MaxStepUp,
			MaxStepDown:         resolved.Policy.MaxStepDown,
			CooldownWindow:      resolved.Policy.CooldownWindow,
			LastRecommendation:  lastRecommended,
			NodeHeadroomMode:    resolved.Policy.NodeHeadroomMode,
			NodeHeadroom:        headroomAt(resolved.Options.HeadroomTimeline, stepObs.At),
			Telemetry:           telemetryStatusFromReadiness(stepReadiness),
			BlackoutWindows:     replayWindows(resolved.Policy.BlackoutWindows, resolved.Policy.KnownEvents),
			DependencyHealth:    append([]safety.DependencyHealthStatus(nil), resolved.Policy.DependencyHealth...),
		})
		if err != nil {
			return Result{}, err
		}

		evaluation.Decision = decisionPtr(recommendation.Explanation)
		evaluation.State = string(recommendation.State)
		evaluation.Message = recommendation.Explanation.Outcome.Message
		evaluation.SuppressionReasons = recommendation.SuppressionReasons
		shouldSurfaceEvent := recommendation.State == recommend.StateAvailable &&
			recommendation.FinalRecommendedReplicas != currentSimulated &&
			(lastRecommended == nil || lastRecommended.RecommendedReplicas != recommendation.FinalRecommendedReplicas)
		var activationTime *time.Time
		if shouldSurfaceEvent {
			readyAt := stepObs.At.Add(resolved.Policy.Warmup)
			activationTime = timePtr(readyAt)
			evaluation.ActivationTime = activationTime
			pending = append(pending, scheduledActivation{
				At:       readyAt,
				Replicas: recommendation.FinalRecommendedReplicas,
			})
			if lastRecommended == nil || lastRecommended.RecommendedReplicas != recommendation.FinalRecommendedReplicas {
				lastRecommended = &safety.PreviousRecommendation{
					RecommendedReplicas: recommendation.FinalRecommendedReplicas,
					ChangedAt:           stepObs.At,
				}
			}
		}
		for _, reason := range recommendation.SuppressionReasons {
			if reason.Code != "" {
				suppressionCounts[reason.Code]++
			}
		}

		requiredReplicas, capacity, capacityKnown := estimateRequiredReplicas(observed, stepObs.At, resolved.Options.CapacityLookback, resolved.Options.MinimumCapacitySamples, resolved.Policy.TargetUtilization)
		evaluation.CapacityEstimated = capacityKnown
		evaluation.CapacityPerReplica = capacity
		if capacityKnown {
			evaluation.RequiredReplicasProxy = int32Ptr(requiredReplicas)
			if evaluation.Decision != nil {
				decision := *evaluation.Decision
				decision.Signals.RequiredReplicasProxy = int32Ptr(requiredReplicas)
				if decision.Suppression != nil {
					decision.Suppression.Signals.RequiredReplicasProxy = int32Ptr(requiredReplicas)
				}
				evaluation.Decision = &decision
			}
		} else {
			evaluation.Caveats = append(evaluation.Caveats, "Required-replica proxy could not be estimated from trailing demand and observed replica history.")
		}
		if capacityKnown {
			evaluation.BaselineOverloaded = stepObs.Replicas < requiredReplicas
			evaluation.ReplayOverloaded = currentSimulated < requiredReplicas
			evaluation.BaselineExcess = stepObs.Replicas > requiredReplicas
			evaluation.ReplayExcess = currentSimulated > requiredReplicas
			intervalMinutes := stepIntervalMinutes(observed, index, resolved.Window.End)
			scoredMinutes += intervalMinutes
			if evaluation.BaselineOverloaded {
				baselineOverloadMinutes += intervalMinutes
			}
			if evaluation.ReplayOverloaded {
				replayOverloadMinutes += intervalMinutes
			}
			if evaluation.BaselineExcess {
				baselineExcessMinutes += intervalMinutes
			}
			if evaluation.ReplayExcess {
				replayExcessMinutes += intervalMinutes
			}
		} else {
			unscoredMinutes += stepIntervalMinutes(observed, index, resolved.Window.End)
		}

		if shouldSurfaceEvent {
			signals := recommendation.Explanation.Signals
			signals.RequiredReplicasProxy = evaluation.RequiredReplicasProxy
			events = append(events, RecommendationEvent{
				Workload:         workloadRef,
				EvaluatedAt:      stepObs.At,
				ActivationTime:   activationTime,
				BaselineReplicas: stepObs.Replicas,
				ReplayReplicas:   currentSimulated,
				BaselineOverload: evaluation.BaselineOverloaded,
				ReplayOverload:   evaluation.ReplayOverloaded,
				BaselineExcess:   evaluation.BaselineExcess,
				ReplayExcess:     evaluation.ReplayExcess,
				Signals:          signals,
				Forecast:         forecastSummary,
				Telemetry:        cloneTelemetrySummaryPtr(stepTelemetrySummary),
				Recommendation: explain.RecommendationSurface{
					State:               recommendation.Explanation.Outcome.State,
					CurrentReplicas:     currentSimulated,
					RecommendedReplicas: recommendation.FinalRecommendedReplicas,
					Delta:               recommendation.Delta,
					Message:             recommendation.Explanation.Outcome.Message,
				},
				BoundsApplied: recommendation.Explanation.BoundsApplied,
				NodeHeadroom:  recommendation.Explanation.Inputs.NodeHeadroom,
				Summary:       recommendation.Explanation.Summary,
			})
		}

		evaluations = append(evaluations, evaluation)

		if index == len(observed)-1 || stepObs.At.Equal(resolved.Window.End) {
			continue
		}
	}

	result.Evaluations = evaluations
	result.RecommendationEvents = events
	result.Replay = buildReplaySummary(replayObs, evaluations, events, suppressionCounts, forecastCounts, reliabilityCounts, resolved.Window)
	result.Baseline.OverloadMinutesProxy = baselineOverloadMinutes
	result.Baseline.ExcessHeadroomMinutesProxy = baselineExcessMinutes
	result.Baseline.ScoredMinutes = scoredMinutes
	result.Baseline.UnscoredMinutes = unscoredMinutes
	result.Replay.OverloadMinutesProxy = replayOverloadMinutes
	result.Replay.ExcessHeadroomMinutesProxy = replayExcessMinutes
	result.Replay.ScoredMinutes = scoredMinutes
	result.Replay.UnscoredMinutes = unscoredMinutes
	result.Replay.Mode = SummaryModeSimulatedReplay
	result.Replay.StartReplicas = baselineSummary.StartReplicas
	if len(replayObs) > 0 {
		result.Replay.StartReplicas = replayObs[0].Replicas
		result.Replay.EndReplicas = replayObs[len(replayObs)-1].Replicas
		if minSimulated < result.Replay.MinReplicas || result.Replay.MinReplicas == 0 {
			result.Replay.MinReplicas = minSimulated
		}
		if maxSimulated > result.Replay.MaxReplicas {
			result.Replay.MaxReplicas = maxSimulated
		}
		result.Replay.MeanReplicas = sumSimulated / float64(len(replayObs))
	}
	result.Replay.ScaleUpEvents = replayScaleUps
	result.Replay.ScaleDownEvents = replayScaleDowns
	if unscoredMinutes > 0 {
		result.Caveats = append(
			result.Caveats,
			fmt.Sprintf(
				"Outcome proxy scoring was unavailable for %.2f of %.2f replay minutes because required-replica capacity could not be estimated from observed demand and replica history.",
				unscoredMinutes,
				scoredMinutes+unscoredMinutes,
			),
		)
	}
	result.Caveats = dedupeStrings(result.Caveats)
	result.ConfidenceNotes = buildConfidenceNotes(result.TelemetryReadiness, reliabilityCounts, advisoryCounts)
	result.Status = StatusComplete
	// Replay should fail closed when it cannot score any overload or excess-headroom proxy minutes.
	// Returning zero deltas in that case would look precise but would be unsupported by the historical evidence.
	if scoredMinutes == 0 && len(result.Evaluations) > 0 {
		result.Status = StatusUnsupported
		result.UnsupportedReasons = append(
			result.UnsupportedReasons,
			"replay could not estimate required-replica proxy anywhere in the requested window",
		)
	}
	if len(result.Evaluations) == 0 {
		result.Status = StatusUnsupported
		result.UnsupportedReasons = append(result.UnsupportedReasons, "replay produced no usable historical evaluations")
	}
	result.UnsupportedReasons = dedupeStrings(result.UnsupportedReasons)
	return result, nil
}

func (s Spec) withDefaults() (Spec, error) {
	if s.Target.Name == "" {
		return Spec{}, fmt.Errorf("%w: target name is required", ErrInvalidSpec)
	}
	if s.Window.Start.IsZero() || s.Window.End.IsZero() || !s.Window.End.After(s.Window.Start) {
		return Spec{}, fmt.Errorf("%w: replay window must have start before end", ErrInvalidSpec)
	}
	if s.Step <= 0 {
		s.Step = time.Minute
	}
	if s.Lookback <= 0 {
		s.Lookback = 30 * time.Minute
	}

	policy := s.Policy
	if policy.Workload == "" {
		if s.Target.Namespace != "" {
			policy.Workload = s.Target.Namespace + "/" + s.Target.Name
		} else {
			policy.Workload = s.Target.Name
		}
	}
	if policy.ForecastHorizon <= 0 {
		policy.ForecastHorizon = 5 * time.Minute
	}
	if policy.Warmup < 0 {
		return Spec{}, fmt.Errorf("%w: warmup must not be negative", ErrInvalidSpec)
	}
	if policy.Warmup == 0 {
		policy.Warmup = 45 * time.Second
	}
	if policy.TargetUtilization <= 0 || policy.TargetUtilization > 1 {
		policy.TargetUtilization = 0.8
	}
	if policy.ConfidenceThreshold <= 0 || policy.ConfidenceThreshold > 1 {
		policy.ConfidenceThreshold = 0.7
	}
	if policy.MinReplicas < 1 {
		policy.MinReplicas = 1
	}
	if policy.MaxReplicas < 1 {
		policy.MaxReplicas = maxInt32(policy.MinReplicas, 10)
	}
	if policy.MaxReplicas < policy.MinReplicas {
		return Spec{}, fmt.Errorf("%w: max replicas must be greater than or equal to min replicas", ErrInvalidSpec)
	}
	if policy.CooldownWindow < 0 {
		return Spec{}, fmt.Errorf("%w: cooldown window must not be negative", ErrInvalidSpec)
	}
	if policy.NodeHeadroomMode == "" {
		policy.NodeHeadroomMode = safety.NodeHeadroomModeDisabled
	}

	options := s.Options
	if options.BaselineMode == "" {
		options.BaselineMode = BaselineModeObservedReplicas
	}
	if options.CapacityLookback <= 0 {
		options.CapacityLookback = minDuration(s.Lookback, 15*time.Minute)
	}
	if options.MinimumCapacitySamples < 1 {
		options.MinimumCapacitySamples = 3
	}
	readinessOptions := options.ReadinessOptions
	if readinessOptions.MinimumLookback == 0 {
		readinessOptions.MinimumLookback = s.Lookback
	}
	if readinessOptions.ExpectedResolution == 0 {
		readinessOptions.ExpectedResolution = s.Step
	}
	options.ReadinessOptions = readinessOptions

	s.Policy = policy
	s.Options = options
	return s, nil
}

func newResult(spec Spec) Result {
	return Result{
		GeneratedAt:     time.Now().UTC(),
		Target:          TargetRef{Namespace: spec.Target.Namespace, Name: spec.Target.Name},
		Window:          WindowSummary{Start: spec.Window.Start, End: spec.Window.End},
		StepSeconds:     int64(spec.Step / time.Second),
		LookbackSeconds: int64(spec.Lookback / time.Second),
		Policy: PolicySummary{
			Workload:                   spec.Policy.Workload,
			ForecastHorizonSeconds:     int64(spec.Policy.ForecastHorizon / time.Second),
			ForecastSeasonalitySeconds: int64(spec.Policy.ForecastSeasonality / time.Second),
			WarmupSeconds:              int64(spec.Policy.Warmup / time.Second),
			TargetUtilization:          spec.Policy.TargetUtilization,
			ConfidenceThreshold:        spec.Policy.ConfidenceThreshold,
			MinReplicas:                spec.Policy.MinReplicas,
			MaxReplicas:                spec.Policy.MaxReplicas,
			MaxStepUp:                  spec.Policy.MaxStepUp,
			MaxStepDown:                spec.Policy.MaxStepDown,
			CooldownSeconds:            int64(spec.Policy.CooldownWindow / time.Second),
			NodeHeadroomMode:           string(spec.Policy.NodeHeadroomMode),
		},
		Baseline: BaselineSummary{
			Mode: SummaryModeObservedReplicas,
		},
		Replay: ReplaySummary{
			Mode:                    SummaryModeSimulatedReplay,
			SuppressionReasonCounts: map[string]int{},
			ForecastModelCounts:     map[string]int{},
			ReliabilityCounts:       map[string]int{},
		},
	}
}

func buildObservedGrid(snapshot metrics.Snapshot, grid []time.Time) ([]observation, error) {
	out := make([]observation, 0, len(grid))
	for _, at := range grid {
		demand, ok := valueAt(snapshot.Demand.Samples, at)
		if !ok {
			return nil, fmt.Errorf("%w: no demand sample available at or before %s", ErrInvalidSpec, at.UTC().Format(time.RFC3339))
		}
		replicas, ok := valueAt(snapshot.Replicas.Samples, at)
		if !ok {
			return nil, fmt.Errorf("%w: no replica sample available at or before %s", ErrInvalidSpec, at.UTC().Format(time.RFC3339))
		}
		out = append(out, observation{
			At:       at,
			Demand:   demand,
			Replicas: maxInt32(0, int32(math.Round(replicas))),
		})
	}
	return out, nil
}

func buildBaselineSummary(observed []observation, replayWindow metrics.Window) (BaselineSummary, error) {
	replayObserved := filterObservations(observed, replayWindow)
	if len(replayObserved) == 0 {
		return BaselineSummary{}, fmt.Errorf("observed replica series does not cover the replay window")
	}
	summary := summarizeObservations(replayObserved, replayWindow)
	return BaselineSummary{
		Mode:    SummaryModeObservedReplicas,
		Summary: summary,
	}, nil
}

func buildReplaySummary(observed []observation, evaluations []Evaluation, events []RecommendationEvent, suppressionCounts, forecastCounts, reliabilityCounts map[string]int, replayWindow metrics.Window) ReplaySummary {
	replayObserved := filterObservations(observed, replayWindow)
	summary := ReplaySummary{
		Mode:                     SummaryModeSimulatedReplay,
		Summary:                  summarizeObservations(replayObserved, replayWindow),
		EvaluationCount:          len(evaluations),
		RecommendationEventCount: len(events),
		SuppressionReasonCounts:  cloneCounts(suppressionCounts),
		ForecastModelCounts:      cloneCounts(forecastCounts),
		ReliabilityCounts:        cloneCounts(reliabilityCounts),
	}
	for _, evaluation := range evaluations {
		switch evaluation.State {
		case string(recommend.StateAvailable):
			summary.AvailableCount++
		case string(recommend.StateSuppressed):
			summary.SuppressedCount++
		default:
			summary.UnavailableCount++
		}
	}
	return summary
}

func summarizeObservations(observed []observation, replayWindow metrics.Window) Summary {
	if len(observed) == 0 {
		return Summary{}
	}
	summary := Summary{
		StartReplicas: observed[0].Replicas,
		EndReplicas:   observed[len(observed)-1].Replicas,
		MinReplicas:   observed[0].Replicas,
		MaxReplicas:   observed[0].Replicas,
	}
	var replicaSum float64
	for index, entry := range observed {
		replicaSum += float64(entry.Replicas)
		if entry.Replicas < summary.MinReplicas {
			summary.MinReplicas = entry.Replicas
		}
		if entry.Replicas > summary.MaxReplicas {
			summary.MaxReplicas = entry.Replicas
		}
		if index == 0 {
			continue
		}
		if entry.Replicas > observed[index-1].Replicas {
			summary.ScaleUpEvents++
		}
		if entry.Replicas < observed[index-1].Replicas {
			summary.ScaleDownEvents++
		}
	}
	summary.MeanReplicas = replicaSum / float64(len(observed))
	return summary
}

func stepIntervalMinutes(observed []observation, index int, windowEnd time.Time) float64 {
	if index < 0 || index >= len(observed) {
		return 0
	}
	current := observed[index].At
	next := windowEnd
	if index+1 < len(observed) {
		next = observed[index+1].At
	}
	if next.After(windowEnd) {
		next = windowEnd
	}
	if !next.After(current) {
		return 0
	}
	return next.Sub(current).Minutes()
}

func applyScheduledActivations(current int32, pending []scheduledActivation, at time.Time, scaleUps, scaleDowns int) (int32, int, int) {
	updated := current
	for _, activation := range pending {
		if activation.At.After(at) {
			continue
		}
		if activation.Replicas > updated {
			scaleUps++
		}
		if activation.Replicas < updated {
			scaleDowns++
		}
		updated = activation.Replicas
	}
	return updated, scaleUps, scaleDowns
}

func pendingAfter(pending []scheduledActivation, at time.Time) []scheduledActivation {
	remaining := make([]scheduledActivation, 0, len(pending))
	for _, activation := range pending {
		if activation.At.After(at) {
			remaining = append(remaining, activation)
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		return remaining[i].At.Before(remaining[j].At)
	})
	return remaining
}

func estimateRequiredReplicas(observed []observation, evaluatedAt time.Time, lookback time.Duration, minimumSamples int, targetUtilization float64) (int32, float64, bool) {
	start := evaluatedAt.Add(-lookback)
	capacities := make([]float64, 0, minimumSamples)
	var demand float64
	for _, entry := range observed {
		if entry.At.Before(start) || entry.At.After(evaluatedAt) {
			continue
		}
		if entry.At.Equal(evaluatedAt) {
			demand = entry.Demand
		}
		if entry.Replicas < 1 || entry.Demand <= 0 {
			continue
		}
		capacities = append(capacities, entry.Demand/(float64(entry.Replicas)*targetUtilization))
	}
	if len(capacities) < minimumSamples {
		return 0, 0, false
	}
	capacity := medianFloat(capacities)
	if capacity <= 0 {
		return 0, 0, false
	}
	if demand <= 0 {
		return 0, capacity, true
	}
	required := int32(math.Ceil(demand / capacity))
	return required, capacity, true
}

func buildStepGrid(start, end time.Time, step time.Duration) []time.Time {
	if step <= 0 || !end.After(start) {
		return nil
	}
	grid := make([]time.Time, 0, int(end.Sub(start)/step)+2)
	for current := start; current.Before(end); current = current.Add(step) {
		grid = append(grid, current.UTC())
	}
	grid = append(grid, end.UTC())
	return dedupeTimes(grid)
}

func filterObservations(observed []observation, replayWindow metrics.Window) []observation {
	filtered := make([]observation, 0, len(observed))
	for _, entry := range observed {
		if entry.At.Before(replayWindow.Start) || entry.At.After(replayWindow.End) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func sliceSnapshot(snapshot metrics.Snapshot, window metrics.Window) metrics.Snapshot {
	return metrics.Snapshot{
		Window:       window,
		Demand:       sliceRequiredSeries(snapshot.Demand, window),
		Replicas:     sliceRequiredSeries(snapshot.Replicas, window),
		CPU:          sliceOptionalSeries(snapshot.CPU, window),
		Memory:       sliceOptionalSeries(snapshot.Memory, window),
		Latency:      sliceOptionalSeries(snapshot.Latency, window),
		Errors:       sliceOptionalSeries(snapshot.Errors, window),
		Warmup:       sliceOptionalSeries(snapshot.Warmup, window),
		NodeHeadroom: sliceOptionalSeries(snapshot.NodeHeadroom, window),
	}
}

func sliceRequiredSeries(series metrics.SignalSeries, window metrics.Window) metrics.SignalSeries {
	return metrics.SignalSeries{
		Name:                    series.Name,
		Unit:                    series.Unit,
		ObservedLabelSignatures: append([]string(nil), series.ObservedLabelSignatures...),
		Samples:                 sliceSamples(series.Samples, window),
	}
}

func sliceOptionalSeries(series *metrics.SignalSeries, window metrics.Window) *metrics.SignalSeries {
	if series == nil {
		return nil
	}
	sliced := sliceRequiredSeries(*series, window)
	return &sliced
}

func sliceSamples(samples []metrics.Sample, window metrics.Window) []metrics.Sample {
	out := make([]metrics.Sample, 0, len(samples))
	for _, sample := range samples {
		if sample.Timestamp.Before(window.Start) || sample.Timestamp.After(window.End) {
			continue
		}
		out = append(out, sample)
	}
	return out
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
	return &safety.TelemetryStatus{
		Level:   level,
		Message: report.Summary,
		Reasons: append([]string(nil), report.Reasons...),
	}
}

func buildConfidenceNotes(readiness explain.TelemetryReadinessSummary, reliabilityCounts, advisoryCounts map[string]int) []string {
	notes := make([]string, 0, 4)
	switch readiness.State {
	case "degraded":
		notes = append(notes, "Telemetry was degraded for part of the replay window; recommendation confidence should be treated cautiously.")
	case "unsupported":
		notes = append(notes, "Telemetry was not sufficient for a supported replay across the requested window.")
	}
	if reliabilityCounts != nil {
		if count := reliabilityCounts[string(forecast.ReliabilityLow)]; count > 0 {
			notes = append(notes, fmt.Sprintf("%d replay evaluations used low-reliability forecasts.", count))
		}
		if count := reliabilityCounts[string(forecast.ReliabilityUnsupported)]; count > 0 {
			notes = append(notes, fmt.Sprintf("%d replay evaluations had unsupported forecast reliability.", count))
		}
	}
	if advisoryCounts != nil {
		if count := advisoryCounts[forecast.AdvisoryLimitedHistory]; count > 0 {
			notes = append(notes, fmt.Sprintf("%d replay evaluations relied on limited forecast history.", count))
		}
		if count := advisoryCounts[forecast.AdvisoryModelDivergence]; count > 0 {
			notes = append(notes, fmt.Sprintf("%d replay evaluations fell back conservatively because forecast models diverged.", count))
		}
	}
	return dedupeStrings(notes)
}

func cloneTelemetrySummaryPtr(summary explain.TelemetryReadinessSummary) *explain.TelemetryReadinessSummary {
	clone := explain.TelemetryReadinessSummary{
		CheckedAt:       summary.CheckedAt,
		State:           summary.State,
		Message:         summary.Message,
		Reasons:         append([]string(nil), summary.Reasons...),
		BlockingReasons: append([]string(nil), summary.BlockingReasons...),
	}
	if len(summary.Signals) > 0 {
		clone.Signals = append([]explain.TelemetrySignalSummary(nil), summary.Signals...)
	}
	return &clone
}

func valueAt(samples []metrics.Sample, at time.Time) (float64, bool) {
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

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func dedupeTimes(values []time.Time) []time.Time {
	if len(values) == 0 {
		return nil
	}
	out := make([]time.Time, 0, len(values))
	for _, value := range values {
		if len(out) > 0 && value.Equal(out[len(out)-1]) {
			continue
		}
		out = append(out, value)
	}
	return out
}

func cloneCounts(source map[string]int) map[string]int {
	if len(source) == 0 {
		return map[string]int{}
	}
	out := make(map[string]int, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func headroomAt(observations []HeadroomObservation, at time.Time) *safety.NodeHeadroomSignal {
	var selected *safety.NodeHeadroomSignal
	for _, observation := range observations {
		if observation.ObservedAt.After(at) {
			continue
		}
		candidate := observation.Signal
		selected = &candidate
	}
	if selected == nil && len(observations) > 0 {
		candidate := observations[0].Signal
		selected = &candidate
	}
	return selected
}

func replayWindows(blackoutWindows []safety.BlackoutWindow, knownEvents []KnownEvent) []safety.BlackoutWindow {
	windows := make([]safety.BlackoutWindow, 0, len(blackoutWindows)+len(knownEvents))
	windows = append(windows, blackoutWindows...)
	for _, event := range knownEvents {
		reason := strings.TrimSpace(event.Note)
		if reason == "" {
			reason = "known event window"
		}
		windows = append(windows, safety.BlackoutWindow{
			Name:   "known-event:" + event.Name,
			Start:  event.Start.UTC(),
			End:    event.End.UTC(),
			Reason: reason,
		})
	}
	return windows
}

func int32Ptr(value int32) *int32 {
	return &value
}

func durationPtr(value time.Duration) *time.Duration {
	return &value
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func decisionPtr(value explain.Decision) *explain.Decision {
	return &value
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
