package recommend

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/oswalpalash/skale/internal/explain"
	"github.com/oswalpalash/skale/internal/safety"
)

const capacityEpsilon = 1e-9

var ErrInvalidInput = errors.New("invalid recommendation input")

type State string

const (
	StateAvailable   State = "available"
	StateSuppressed  State = "suppressed"
	StateUnavailable State = "unavailable"
)

// Input contains the inputs required to size a v1 predictive recommendation.
type Input struct {
	Workload          string
	WorkloadRef       explain.WorkloadIdentity
	EvaluationTime    time.Time
	ForecastMethod    string
	ForecastedDemand  float64
	ForecastTimestamp time.Time
	ForecastSummary   *explain.ForecastSummary

	CurrentDemand     float64
	CurrentReplicas   int32
	TargetUtilization float64
	EstimatedWarmup   time.Duration
	TelemetrySummary  *explain.TelemetryReadinessSummary
	CapacityEstimate  *CapacityEstimate

	ConfidenceScore     float64
	ConfidenceThreshold float64

	MinReplicas int32
	MaxReplicas int32
	MaxStepUp   *int32
	MaxStepDown *int32

	CooldownWindow     time.Duration
	LastRecommendation *safety.PreviousRecommendation
	NodeHeadroomMode   safety.NodeHeadroomMode
	NodeHeadroom       *safety.NodeHeadroomSignal
	Telemetry          *safety.TelemetryStatus
	ModelDivergence    *safety.ModelDivergenceStatus
	ForecastError      *safety.ForecastErrorStatus
	BlackoutWindows    []safety.BlackoutWindow
	DependencyHealth   []safety.DependencyHealthStatus
	OperatorMode       safety.OperatorMode
	CircuitBreaker     *safety.CircuitBreaker
}

type CapacityEstimate struct {
	Estimated          bool
	PerReplicaCapacity float64
	WindowStart        time.Time
	WindowEnd          time.Time
	SampleCount        int
}

// Result is the bounded recommendation output shared by live and replay flows.
type Result struct {
	State                       State
	DesiredReplicas             int32
	CurrentReplicas             int32
	EvaluationTime              time.Time
	RecommendationTime          time.Time
	TargetReadyTime             time.Time
	EffectivePerReplicaCapacity float64
	RawRequiredReplicas         int32
	PolicyBoundReplicas         int32
	StepBoundReplicas           int32
	FinalRecommendedReplicas    int32
	Delta                       int32
	Suppressed                  bool
	SuppressionReasons          []explain.SuppressionReason
	Explanation                 explain.Decision
}

// Engine converts demand forecasts into safe, explainable replica recommendations.
type Engine interface {
	Recommend(input Input) (Result, error)
}

// DeterministicEngine is the default v1 implementation for recommendation sizing.
type DeterministicEngine struct {
	Safety  safety.Evaluator
	Explain explain.Builder
}

type evaluationState struct {
	effectivePerReplicaCapacity float64
	rawRequiredReplicas         int32
	policyBoundReplicas         int32
	stepBoundReplicas           int32
	finalRecommendedReplicas    int32
	delta                       int32
	minMaxBounded               bool
	stepUpBounded               bool
	stepDownBounded             bool
	boundDetails                []explain.BoundDetail
	confidencePassed            bool
	forecastHorizonPassed       bool
	telemetryReady              *bool
	modelDivergencePassed       *bool
	recentForecastErrorPassed   *bool
	blackoutPassed              bool
	dependencyHealthPassed      *bool
	cooldownPassed              bool
	nodeHeadroomStatus          string
	nodeHeadroomPassed          *bool
	nodeHeadroom                *safety.NodeHeadroomAssessment
	operatorMode                string
	operatorEnabled             bool
	circuitBreakerClosed        *bool
	state                       State
	suppressed                  bool
	suppressionReasons          []explain.SuppressionReason
}

func (e DeterministicEngine) Recommend(input Input) (Result, error) {
	if err := input.Validate(); err != nil {
		return Result{}, err
	}

	builder := e.Explain
	if builder == nil {
		builder = explain.DefaultBuilder{}
	}
	evaluator := e.Safety
	if evaluator == nil {
		evaluator = safety.DefaultEvaluator{}
	}

	targetReadyTime := input.EvaluationTime.Add(input.EstimatedWarmup)
	operatorMode := input.OperatorMode
	if operatorMode == "" {
		operatorMode = safety.OperatorModeEnabled
	}
	state := evaluationState{
		confidencePassed:      input.ConfidenceScore >= input.ConfidenceThreshold,
		forecastHorizonPassed: !input.ForecastTimestamp.Before(targetReadyTime),
		blackoutPassed:        true,
		cooldownPassed:        true,
		operatorMode:          string(operatorMode),
		operatorEnabled:       operatorMode == safety.OperatorModeEnabled,
	}

	if !state.forecastHorizonPassed {
		state.suppressionReasons = append(state.suppressionReasons, explain.SuppressionReason{
			Code:     explain.ReasonForecastHorizonTooShort,
			Category: explain.SuppressionCategoryAvailability,
			Severity: explain.SuppressionSeverityError,
			Message:  fmt.Sprintf("forecast timestamp %s does not cover target ready time %s", input.ForecastTimestamp.UTC().Format(time.RFC3339), targetReadyTime.UTC().Format(time.RFC3339)),
		})
	}
	if input.CurrentReplicas < 1 && input.ForecastedDemand > 0 {
		state.suppressionReasons = append(state.suppressionReasons, explain.SuppressionReason{
			Code:     explain.ReasonNoCapacityBaseline,
			Category: explain.SuppressionCategoryAvailability,
			Severity: explain.SuppressionSeverityError,
			Message:  "current replicas must be at least 1 to size positive forecasted demand",
		})
	}
	if input.CurrentDemand <= 0 && input.ForecastedDemand > 0 && input.CapacityEstimate == nil {
		state.suppressionReasons = append(state.suppressionReasons, explain.SuppressionReason{
			Code:     explain.ReasonNoCurrentDemandBaseline,
			Category: explain.SuppressionCategoryAvailability,
			Severity: explain.SuppressionSeverityError,
			Message:  "current demand must be positive to size positive forecasted demand from observed capacity",
		})
	}
	if input.ForecastedDemand > 0 && input.CapacityEstimate != nil && (!input.CapacityEstimate.Estimated || input.CapacityEstimate.PerReplicaCapacity <= 0) {
		state.suppressionReasons = append(state.suppressionReasons, explain.SuppressionReason{
			Code:     explain.ReasonNoCapacityBaseline,
			Category: explain.SuppressionCategoryAvailability,
			Severity: explain.SuppressionSeverityError,
			Message:  "stable per-replica capacity could not be estimated from the recent demand and replica window",
		})
	}

	if len(state.suppressionReasons) > 0 {
		state.state = StateUnavailable
		return buildResult(input, state, builder), nil
	}

	state.effectivePerReplicaCapacity = effectivePerReplicaCapacity(input.CurrentDemand, input.CurrentReplicas, input.TargetUtilization)
	if input.CapacityEstimate != nil {
		state.effectivePerReplicaCapacity = input.CapacityEstimate.PerReplicaCapacity
	}
	if input.CurrentDemand <= 0 && input.ForecastedDemand <= 0 {
		state.effectivePerReplicaCapacity = 0
		state.rawRequiredReplicas = 0
	} else {
		state.rawRequiredReplicas = requiredReplicas(input.ForecastedDemand, state.effectivePerReplicaCapacity)
	}

	safetyResult, err := evaluator.Evaluate(safety.Input{
		EvaluationTime:      input.EvaluationTime,
		CurrentReplicas:     input.CurrentReplicas,
		RawProposedReplicas: state.rawRequiredReplicas,
		MinReplicas:         input.MinReplicas,
		MaxReplicas:         input.MaxReplicas,
		MaxStepUp:           input.MaxStepUp,
		MaxStepDown:         input.MaxStepDown,
		ConfidenceScore:     input.ConfidenceScore,
		ConfidenceThreshold: input.ConfidenceThreshold,
		Telemetry:           input.Telemetry,
		ModelDivergence:     input.ModelDivergence,
		ForecastError:       input.ForecastError,
		BlackoutWindows:     input.BlackoutWindows,
		DependencyHealth:    input.DependencyHealth,
		CooldownWindow:      input.CooldownWindow,
		LastRecommendation:  input.LastRecommendation,
		NodeHeadroomMode:    input.NodeHeadroomMode,
		NodeHeadroom:        input.NodeHeadroom,
		OperatorMode:        input.OperatorMode,
		CircuitBreaker:      input.CircuitBreaker,
	})
	if err != nil {
		return Result{}, err
	}

	state.policyBoundReplicas = safetyResult.PolicyBoundReplicas
	state.stepBoundReplicas = safetyResult.StepBoundReplicas
	state.finalRecommendedReplicas = safetyResult.FinalProposedReplicas
	state.delta = state.finalRecommendedReplicas - input.CurrentReplicas
	state.minMaxBounded = safetyResult.MinMaxBounded
	state.stepUpBounded = safetyResult.StepUpBounded
	state.stepDownBounded = safetyResult.StepDownBounded
	state.boundDetails = safetyResult.BoundDetails
	state.confidencePassed = safetyResult.Checks.ConfidencePassed
	state.telemetryReady = safetyResult.Checks.TelemetryReady
	state.modelDivergencePassed = safetyResult.Checks.ModelDivergencePassed
	state.recentForecastErrorPassed = safetyResult.Checks.RecentForecastErrorPassed
	state.blackoutPassed = safetyResult.Checks.BlackoutPassed
	state.dependencyHealthPassed = safetyResult.Checks.DependencyHealthPassed
	state.cooldownPassed = safetyResult.Checks.CooldownPassed
	if safetyResult.Checks.NodeHeadroomStatus != nil {
		state.nodeHeadroomStatus = string(*safetyResult.Checks.NodeHeadroomStatus)
	}
	state.nodeHeadroomPassed = safetyResult.Checks.NodeHeadroomPassed
	state.nodeHeadroom = safetyResult.NodeHeadroom
	state.operatorMode = string(safetyResult.Checks.OperatorMode)
	state.operatorEnabled = safetyResult.Checks.OperatorEnabled
	state.circuitBreakerClosed = safetyResult.Checks.CircuitBreakerClosed
	state.suppressionReasons = append(state.suppressionReasons, safetyResult.Reasons...)
	state.suppressionReasons = dedupeReasons(state.suppressionReasons)
	if len(state.suppressionReasons) > 0 {
		state.state = StateSuppressed
		state.suppressed = true
	} else {
		state.state = StateAvailable
	}

	return buildResult(input, state, builder), nil
}

// Validate checks the input invariants for the v1 recommendation engine.
func (i Input) Validate() error {
	if i.EvaluationTime.IsZero() {
		return fmt.Errorf("%w: evaluation time is required", ErrInvalidInput)
	}
	if i.ForecastTimestamp.IsZero() {
		return fmt.Errorf("%w: forecast timestamp is required", ErrInvalidInput)
	}
	if i.ForecastedDemand < 0 {
		return fmt.Errorf("%w: forecasted demand must be non-negative", ErrInvalidInput)
	}
	if i.CurrentDemand < 0 {
		return fmt.Errorf("%w: current demand must be non-negative", ErrInvalidInput)
	}
	if i.CurrentReplicas < 0 {
		return fmt.Errorf("%w: current replicas must be non-negative", ErrInvalidInput)
	}
	if i.TargetUtilization <= 0 || i.TargetUtilization > 1 {
		return fmt.Errorf("%w: target utilization must be in the range (0, 1]", ErrInvalidInput)
	}
	if i.EstimatedWarmup < 0 {
		return fmt.Errorf("%w: estimated warmup must be non-negative", ErrInvalidInput)
	}
	if i.ConfidenceScore < 0 || i.ConfidenceScore > 1 {
		return fmt.Errorf("%w: confidence score must be in the range [0, 1]", ErrInvalidInput)
	}
	if i.ConfidenceThreshold < 0 || i.ConfidenceThreshold > 1 {
		return fmt.Errorf("%w: confidence threshold must be in the range [0, 1]", ErrInvalidInput)
	}
	if i.MinReplicas < 1 {
		return fmt.Errorf("%w: min replicas must be at least 1", ErrInvalidInput)
	}
	if i.MaxReplicas < 1 {
		return fmt.Errorf("%w: max replicas must be at least 1", ErrInvalidInput)
	}
	if i.MinReplicas > i.MaxReplicas {
		return fmt.Errorf("%w: min replicas must be less than or equal to max replicas", ErrInvalidInput)
	}
	if i.MaxStepUp != nil && *i.MaxStepUp < 1 {
		return fmt.Errorf("%w: max step-up must be at least 1 when set", ErrInvalidInput)
	}
	if i.MaxStepDown != nil && *i.MaxStepDown < 1 {
		return fmt.Errorf("%w: max step-down must be at least 1 when set", ErrInvalidInput)
	}
	if i.CooldownWindow < 0 {
		return fmt.Errorf("%w: cooldown window must be non-negative", ErrInvalidInput)
	}
	return nil
}

func buildResult(input Input, state evaluationState, builder explain.Builder) Result {
	result := Result{
		State:                       state.state,
		DesiredReplicas:             state.finalRecommendedReplicas,
		CurrentReplicas:             input.CurrentReplicas,
		EvaluationTime:              input.EvaluationTime,
		RecommendationTime:          input.EvaluationTime,
		TargetReadyTime:             input.EvaluationTime.Add(input.EstimatedWarmup),
		EffectivePerReplicaCapacity: state.effectivePerReplicaCapacity,
		RawRequiredReplicas:         state.rawRequiredReplicas,
		PolicyBoundReplicas:         state.policyBoundReplicas,
		StepBoundReplicas:           state.stepBoundReplicas,
		FinalRecommendedReplicas:    state.finalRecommendedReplicas,
		Delta:                       state.delta,
		Suppressed:                  state.suppressed,
		SuppressionReasons:          state.suppressionReasons,
	}

	result.Explanation = builder.Build(explain.BuildInput{
		Workload:                    input.Workload,
		WorkloadRef:                 input.WorkloadRef,
		EvaluationTime:              result.EvaluationTime,
		RecommendationTime:          result.RecommendationTime,
		TargetReadyTime:             result.TargetReadyTime,
		ForecastMethod:              input.ForecastMethod,
		ForecastedDemand:            input.ForecastedDemand,
		ForecastTimestamp:           input.ForecastTimestamp,
		ForecastSummary:             input.ForecastSummary,
		CurrentDemand:               input.CurrentDemand,
		CurrentReplicas:             input.CurrentReplicas,
		TargetUtilization:           input.TargetUtilization,
		Warmup:                      input.EstimatedWarmup,
		Telemetry:                   input.TelemetrySummary,
		CapacityWindowStart:         capacityWindowStart(input.CapacityEstimate),
		CapacityWindowEnd:           capacityWindowEnd(input.CapacityEstimate),
		CapacitySampleCount:         capacitySampleCount(input.CapacityEstimate),
		ConfidenceScore:             input.ConfidenceScore,
		ConfidenceThreshold:         input.ConfidenceThreshold,
		MinReplicas:                 input.MinReplicas,
		MaxReplicas:                 input.MaxReplicas,
		MaxStepUp:                   input.MaxStepUp,
		MaxStepDown:                 input.MaxStepDown,
		CooldownWindow:              input.CooldownWindow,
		NodeHeadroom:                buildExplainHeadroomAssessment(state.nodeHeadroom),
		EffectivePerReplicaCapacity: state.effectivePerReplicaCapacity,
		RawRequiredReplicas:         state.rawRequiredReplicas,
		PolicyBoundReplicas:         state.policyBoundReplicas,
		StepBoundReplicas:           state.stepBoundReplicas,
		FinalRecommendedReplicas:    state.finalRecommendedReplicas,
		Delta:                       state.delta,
		MinMaxBounded:               state.minMaxBounded,
		StepUpBounded:               state.stepUpBounded,
		StepDownBounded:             state.stepDownBounded,
		BoundDetails:                state.boundDetails,
		ConfidencePassed:            state.confidencePassed,
		ForecastHorizonPassed:       state.forecastHorizonPassed,
		TelemetryReady:              state.telemetryReady,
		ModelDivergencePassed:       state.modelDivergencePassed,
		RecentForecastErrorPassed:   state.recentForecastErrorPassed,
		BlackoutPassed:              state.blackoutPassed,
		DependencyHealthPassed:      state.dependencyHealthPassed,
		CooldownPassed:              state.cooldownPassed,
		NodeHeadroomStatus:          state.nodeHeadroomStatus,
		NodeHeadroomPassed:          state.nodeHeadroomPassed,
		OperatorMode:                state.operatorMode,
		OperatorEnabled:             state.operatorEnabled,
		CircuitBreakerClosed:        state.circuitBreakerClosed,
		State:                       string(state.state),
		Suppressed:                  state.suppressed,
		SuppressionReasons:          state.suppressionReasons,
	})

	return result
}

func capacityWindowStart(estimate *CapacityEstimate) time.Time {
	if estimate == nil {
		return time.Time{}
	}
	return estimate.WindowStart
}

func capacityWindowEnd(estimate *CapacityEstimate) time.Time {
	if estimate == nil {
		return time.Time{}
	}
	return estimate.WindowEnd
}

func capacitySampleCount(estimate *CapacityEstimate) int {
	if estimate == nil {
		return 0
	}
	return estimate.SampleCount
}

func effectivePerReplicaCapacity(currentDemand float64, currentReplicas int32, targetUtilization float64) float64 {
	if currentDemand <= 0 {
		return 0
	}
	return math.Max(capacityEpsilon, currentDemand/(float64(maxInt32(1, currentReplicas))*targetUtilization))
}

func requiredReplicas(forecastedDemand, perReplicaCapacity float64) int32 {
	if forecastedDemand <= 0 || perReplicaCapacity <= 0 {
		return 0
	}

	required := math.Ceil(forecastedDemand / perReplicaCapacity)
	if required > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(required)
}

func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func buildExplainHeadroomAssessment(assessment *safety.NodeHeadroomAssessment) *explain.NodeHeadroomAssessment {
	if assessment == nil {
		return nil
	}
	return &explain.NodeHeadroomAssessment{
		ObservedAt:                  assessment.ObservedAt,
		Status:                      string(assessment.Status),
		Message:                     assessment.Message,
		AdditionalPodsNeeded:        assessment.AdditionalPodsNeeded,
		EstimatedAdditionalPods:     assessment.EstimatedAdditionalPods,
		EstimatedByClusterResources: assessment.EstimatedByClusterResources,
		EstimatedByNodeSummaries:    assessment.EstimatedByNodeSummaries,
		SchedulableNodeCount:        assessment.SchedulableNodeCount,
		FittingNodeCount:            assessment.FittingNodeCount,
		PodCPURequestMilli:          assessment.PodRequests.CPUMilli,
		PodMemoryRequestBytes:       assessment.PodRequests.MemoryBytes,
		ClusterAvailableCPUMilli:    assessment.ClusterAvailable.CPUMilli,
		ClusterAvailableMemoryBytes: assessment.ClusterAvailable.MemoryBytes,
		LimitingResource:            assessment.LimitingResource,
	}
}

func dedupeReasons(reasons []explain.SuppressionReason) []explain.SuppressionReason {
	seen := make(map[string]struct{}, len(reasons))
	deduped := make([]explain.SuppressionReason, 0, len(reasons))
	for _, reason := range reasons {
		key := reason.Code + "\x00" + reason.Message
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, reason)
	}
	return deduped
}
