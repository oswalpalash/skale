package explain

import (
	"fmt"
	"strings"
	"time"
)

const (
	ReasonLowConfidence            = "low_confidence"
	ReasonTelemetryNotReady        = "telemetry_not_ready"
	ReasonForecastUnavailable      = "forecast_unavailable"
	ReasonModelDivergenceTooHigh   = "model_divergence_too_high"
	ReasonForecastErrorTooHigh     = "recent_forecast_error_too_high"
	ReasonBlackoutWindowActive     = "blackout_window_active"
	ReasonDependencyHealthFailed   = "dependency_health_failed"
	ReasonOperatorPaused           = "operator_paused"
	ReasonOperatorDisabled         = "operator_disabled"
	ReasonCircuitBreakerOpen       = "circuit_breaker_open"
	ReasonForecastHorizonTooShort  = "forecast_horizon_too_short"
	ReasonNoCapacityBaseline       = "no_capacity_baseline"
	ReasonNoCurrentDemandBaseline  = "no_current_demand_baseline"
	ReasonCooldownActive           = "cooldown_active"
	ReasonMissingNodeHeadroom      = "missing_node_headroom"
	ReasonStaleNodeHeadroom        = "stale_node_headroom"
	ReasonUnsupportedNodeHeadroom  = "unsupported_node_headroom"
	ReasonUncertainNodeHeadroom    = "uncertain_node_headroom"
	ReasonInsufficientNodeHeadroom = "insufficient_node_headroom"
)

type SuppressionCategory string

const (
	SuppressionCategoryAvailability  SuppressionCategory = "availability"
	SuppressionCategoryForecast      SuppressionCategory = "forecast"
	SuppressionCategoryTelemetry     SuppressionCategory = "telemetry"
	SuppressionCategoryModel         SuppressionCategory = "model"
	SuppressionCategoryPolicy        SuppressionCategory = "policy"
	SuppressionCategoryDependency    SuppressionCategory = "dependency"
	SuppressionCategoryCluster       SuppressionCategory = "cluster"
	SuppressionCategoryOperator      SuppressionCategory = "operator"
	SuppressionCategoryStabilization SuppressionCategory = "stabilization"
)

type SuppressionSeverity string

const (
	SuppressionSeverityWarning SuppressionSeverity = "warning"
	SuppressionSeverityError   SuppressionSeverity = "error"
)

// SuppressionReason describes why a recommendation was not surfaced safely.
type SuppressionReason struct {
	Code     string              `json:"code,omitempty"`
	Category SuppressionCategory `json:"category,omitempty"`
	Severity SuppressionSeverity `json:"severity,omitempty"`
	Message  string              `json:"message,omitempty"`
}

// BoundDetail describes why a bounded result changed from the raw recommendation.
type BoundDetail struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// Decision is the structured explanation returned for each recommendation evaluation.
type Decision struct {
	Workload           WorkloadIdentity           `json:"workload"`
	EvaluationTime     time.Time                  `json:"evaluationTime,omitempty"`
	RecommendationTime time.Time                  `json:"recommendationTime,omitempty"`
	TargetReadyTime    time.Time                  `json:"targetReadyTime,omitempty"`
	Summary            string                     `json:"summary,omitempty"`
	Signals            SignalSummary              `json:"signals"`
	Forecast           ForecastSummary            `json:"forecast"`
	Telemetry          *TelemetryReadinessSummary `json:"telemetry,omitempty"`
	Inputs             DecisionInputs             `json:"inputs"`
	Derived            DecisionDerived            `json:"derived"`
	BoundsApplied      BoundsApplied              `json:"boundsApplied"`
	SafetyChecks       SafetyChecks               `json:"safetyChecks"`
	Outcome            DecisionOutcome            `json:"outcome"`
	Suppression        *SuppressionExplanation    `json:"suppression,omitempty"`
}

// DecisionInputs records the direct inputs used to size a recommendation.
type DecisionInputs struct {
	ForecastMethod      string                  `json:"forecastMethod,omitempty"`
	ForecastedDemand    float64                 `json:"forecastedDemand,omitempty"`
	ForecastTimestamp   time.Time               `json:"forecastTimestamp,omitempty"`
	CurrentDemand       float64                 `json:"currentDemand,omitempty"`
	CurrentReplicas     int32                   `json:"currentReplicas,omitempty"`
	TargetUtilization   float64                 `json:"targetUtilization,omitempty"`
	WarmupSeconds       int64                   `json:"warmupSeconds,omitempty"`
	ConfidenceScore     float64                 `json:"confidenceScore,omitempty"`
	ConfidenceThreshold float64                 `json:"confidenceThreshold,omitempty"`
	MinReplicas         int32                   `json:"minReplicas,omitempty"`
	MaxReplicas         int32                   `json:"maxReplicas,omitempty"`
	MaxStepUp           *int32                  `json:"maxStepUp,omitempty"`
	MaxStepDown         *int32                  `json:"maxStepDown,omitempty"`
	CooldownSeconds     int64                   `json:"cooldownSeconds,omitempty"`
	NodeHeadroom        *NodeHeadroomAssessment `json:"nodeHeadroom,omitempty"`
}

// NodeHeadroomAssessment is the structured request-based schedulability estimate for a scale-up.
type NodeHeadroomAssessment struct {
	ObservedAt                  time.Time `json:"observedAt,omitempty"`
	Status                      string    `json:"status,omitempty"`
	Message                     string    `json:"message,omitempty"`
	AdditionalPodsNeeded        int32     `json:"additionalPodsNeeded,omitempty"`
	EstimatedAdditionalPods     int32     `json:"estimatedAdditionalPods,omitempty"`
	EstimatedByClusterResources int32     `json:"estimatedByClusterResources,omitempty"`
	EstimatedByNodeSummaries    *int32    `json:"estimatedByNodeSummaries,omitempty"`
	SchedulableNodeCount        *int32    `json:"schedulableNodeCount,omitempty"`
	FittingNodeCount            *int32    `json:"fittingNodeCount,omitempty"`
	PodCPURequestMilli          int64     `json:"podCpuRequestMilli,omitempty"`
	PodMemoryRequestBytes       int64     `json:"podMemoryRequestBytes,omitempty"`
	ClusterAvailableCPUMilli    int64     `json:"clusterAvailableCpuMilli,omitempty"`
	ClusterAvailableMemoryBytes int64     `json:"clusterAvailableMemoryBytes,omitempty"`
	LimitingResource            string    `json:"limitingResource,omitempty"`
}

// DecisionDerived records the deterministic math outputs for a recommendation.
type DecisionDerived struct {
	EffectivePerReplicaCapacity float64 `json:"effectivePerReplicaCapacity,omitempty"`
	RawRequiredReplicas         int32   `json:"rawRequiredReplicas,omitempty"`
	PolicyBoundReplicas         int32   `json:"policyBoundReplicas,omitempty"`
	StepBoundReplicas           int32   `json:"stepBoundReplicas,omitempty"`
	FinalRecommendedReplicas    int32   `json:"finalRecommendedReplicas,omitempty"`
	Delta                       int32   `json:"delta,omitempty"`
}

// BoundsApplied identifies which policy bounds changed the recommendation.
type BoundsApplied struct {
	MinMaxBounded   bool          `json:"minMaxBounded,omitempty"`
	StepUpBounded   bool          `json:"stepUpBounded,omitempty"`
	StepDownBounded bool          `json:"stepDownBounded,omitempty"`
	Details         []BoundDetail `json:"details,omitempty"`
}

// SafetyChecks records the safety gates evaluated for the recommendation.
type SafetyChecks struct {
	ConfidencePassed          bool   `json:"confidencePassed"`
	ForecastHorizonPassed     bool   `json:"forecastHorizonPassed"`
	TelemetryReady            *bool  `json:"telemetryReady,omitempty"`
	ModelDivergencePassed     *bool  `json:"modelDivergencePassed,omitempty"`
	RecentForecastErrorPassed *bool  `json:"recentForecastErrorPassed,omitempty"`
	BlackoutPassed            bool   `json:"blackoutPassed"`
	DependencyHealthPassed    *bool  `json:"dependencyHealthPassed,omitempty"`
	CooldownPassed            bool   `json:"cooldownPassed"`
	NodeHeadroomStatus        string `json:"nodeHeadroomStatus,omitempty"`
	NodeHeadroomPassed        *bool  `json:"nodeHeadroomPassed,omitempty"`
	OperatorMode              string `json:"operatorMode,omitempty"`
	OperatorEnabled           bool   `json:"operatorEnabled"`
	CircuitBreakerClosed      *bool  `json:"circuitBreakerClosed,omitempty"`
}

// DecisionOutcome records the final surfaced decision.
type DecisionOutcome struct {
	State                    string              `json:"state,omitempty"`
	Suppressed               bool                `json:"suppressed,omitempty"`
	FinalRecommendedReplicas int32               `json:"finalRecommendedReplicas,omitempty"`
	SuppressionReasons       []SuppressionReason `json:"suppressionReasons,omitempty"`
	Message                  string              `json:"message,omitempty"`
}

// BuildInput is the data required to construct a Decision explanation.
type BuildInput struct {
	Workload           string
	WorkloadRef        WorkloadIdentity
	EvaluationTime     time.Time
	RecommendationTime time.Time
	TargetReadyTime    time.Time

	ForecastMethod    string
	ForecastedDemand  float64
	ForecastTimestamp time.Time
	ForecastSummary   *ForecastSummary

	CurrentDemand     float64
	CurrentReplicas   int32
	TargetUtilization float64
	Warmup            time.Duration
	Telemetry         *TelemetryReadinessSummary

	ConfidenceScore     float64
	ConfidenceThreshold float64

	MinReplicas int32
	MaxReplicas int32
	MaxStepUp   *int32
	MaxStepDown *int32

	CooldownWindow time.Duration
	NodeHeadroom   *NodeHeadroomAssessment

	EffectivePerReplicaCapacity float64
	RawRequiredReplicas         int32
	PolicyBoundReplicas         int32
	StepBoundReplicas           int32
	FinalRecommendedReplicas    int32
	Delta                       int32

	MinMaxBounded   bool
	StepUpBounded   bool
	StepDownBounded bool
	BoundDetails    []BoundDetail

	ConfidencePassed          bool
	ForecastHorizonPassed     bool
	TelemetryReady            *bool
	ModelDivergencePassed     *bool
	RecentForecastErrorPassed *bool
	BlackoutPassed            bool
	DependencyHealthPassed    *bool
	CooldownPassed            bool
	NodeHeadroomStatus        string
	NodeHeadroomPassed        *bool
	OperatorMode              string
	OperatorEnabled           bool
	CircuitBreakerClosed      *bool

	State              string
	Suppressed         bool
	SuppressionReasons []SuppressionReason
}

// Builder creates structured explanation records from recommendation results.
type Builder interface {
	Build(input BuildInput) Decision
}

// DefaultBuilder builds the default v1 explanation contract.
type DefaultBuilder struct{}

// StaticBuilder is a convenience builder for tests and scaffolding.
type StaticBuilder struct {
	Decision Decision
}

func (b StaticBuilder) Build(BuildInput) Decision {
	return b.Decision
}

func (DefaultBuilder) Build(input BuildInput) Decision {
	workload := input.WorkloadRef
	if workload.IsZero() {
		workload = WorkloadIdentityFromString(input.Workload)
	}
	signals := SignalSummary{
		ObservedAt:                  input.EvaluationTime.UTC(),
		CurrentDemand:               input.CurrentDemand,
		CurrentReplicas:             input.CurrentReplicas,
		TargetUtilization:           input.TargetUtilization,
		EffectivePerReplicaCapacity: input.EffectivePerReplicaCapacity,
		WarmupSeconds:               int64(input.Warmup / time.Second),
	}
	forecastSummary := buildDecisionForecastSummary(input)

	decision := Decision{
		Workload:           workload,
		EvaluationTime:     input.EvaluationTime,
		RecommendationTime: input.RecommendationTime,
		TargetReadyTime:    input.TargetReadyTime,
		Signals:            signals,
		Forecast:           forecastSummary,
		Telemetry:          cloneTelemetrySummary(input.Telemetry),
		Inputs: DecisionInputs{
			ForecastMethod:      input.ForecastMethod,
			ForecastedDemand:    input.ForecastedDemand,
			ForecastTimestamp:   input.ForecastTimestamp,
			CurrentDemand:       input.CurrentDemand,
			CurrentReplicas:     input.CurrentReplicas,
			TargetUtilization:   input.TargetUtilization,
			WarmupSeconds:       int64(input.Warmup / time.Second),
			ConfidenceScore:     input.ConfidenceScore,
			ConfidenceThreshold: input.ConfidenceThreshold,
			MinReplicas:         input.MinReplicas,
			MaxReplicas:         input.MaxReplicas,
			MaxStepUp:           input.MaxStepUp,
			MaxStepDown:         input.MaxStepDown,
			CooldownSeconds:     int64(input.CooldownWindow / time.Second),
			NodeHeadroom:        input.NodeHeadroom,
		},
		Derived: DecisionDerived{
			EffectivePerReplicaCapacity: input.EffectivePerReplicaCapacity,
			RawRequiredReplicas:         input.RawRequiredReplicas,
			PolicyBoundReplicas:         input.PolicyBoundReplicas,
			StepBoundReplicas:           input.StepBoundReplicas,
			FinalRecommendedReplicas:    input.FinalRecommendedReplicas,
			Delta:                       input.Delta,
		},
		BoundsApplied: BoundsApplied{
			MinMaxBounded:   input.MinMaxBounded,
			StepUpBounded:   input.StepUpBounded,
			StepDownBounded: input.StepDownBounded,
			Details:         input.BoundDetails,
		},
		SafetyChecks: SafetyChecks{
			ConfidencePassed:          input.ConfidencePassed,
			ForecastHorizonPassed:     input.ForecastHorizonPassed,
			TelemetryReady:            input.TelemetryReady,
			ModelDivergencePassed:     input.ModelDivergencePassed,
			RecentForecastErrorPassed: input.RecentForecastErrorPassed,
			BlackoutPassed:            input.BlackoutPassed,
			DependencyHealthPassed:    input.DependencyHealthPassed,
			CooldownPassed:            input.CooldownPassed,
			NodeHeadroomStatus:        input.NodeHeadroomStatus,
			NodeHeadroomPassed:        input.NodeHeadroomPassed,
			OperatorMode:              input.OperatorMode,
			OperatorEnabled:           input.OperatorEnabled,
			CircuitBreakerClosed:      input.CircuitBreakerClosed,
		},
		Outcome: DecisionOutcome{
			State:                    input.State,
			Suppressed:               input.Suppressed,
			FinalRecommendedReplicas: input.FinalRecommendedReplicas,
			SuppressionReasons:       input.SuppressionReasons,
		},
	}
	decision.Summary = buildSummary(decision)
	decision.Outcome.Message = decision.Summary
	if input.Suppressed || input.State == "unavailable" {
		decision.Suppression = BuildSuppressionExplanation(
			decision.Workload,
			decision.EvaluationTime,
			decision.Signals,
			decision.Forecast,
			decision.BoundsApplied,
			decision.Outcome.State,
			decision.Outcome.SuppressionReasons,
			decision.Outcome.Message,
		)
	}
	return decision
}

func buildSummary(decision Decision) string {
	method := decision.Forecast.Method
	if method == "" {
		method = "Forecast"
	}

	targetTime := "unknown target time"
	if !decision.Forecast.ForecastFor.IsZero() {
		targetTime = decision.Forecast.ForecastFor.UTC().Format(time.RFC3339)
	} else if !decision.TargetReadyTime.IsZero() {
		targetTime = decision.TargetReadyTime.UTC().Format(time.RFC3339)
	}

	if decision.Outcome.State == "available" {
		if decision.Outcome.FinalRecommendedReplicas == decision.Signals.CurrentReplicas {
			return fmt.Sprintf(
				"%s forecast %.2f for readiness at %s implies no replica change; hold at %d replicas.",
				method,
				decision.Forecast.PredictedDemand,
				targetTime,
				decision.Outcome.FinalRecommendedReplicas,
			)
		}
		return fmt.Sprintf(
			"%s forecast %.2f for readiness at %s implied %d raw replicas; final recommendation %d replicas.",
			method,
			decision.Forecast.PredictedDemand,
			targetTime,
			decision.Derived.RawRequiredReplicas,
			decision.Outcome.FinalRecommendedReplicas,
		)
	}

	codes := strings.Join(ReasonCodes(decision.Outcome.SuppressionReasons), ", ")
	if codes == "" {
		codes = "unspecified_reason"
	}

	if decision.Outcome.State == "unavailable" {
		return fmt.Sprintf(
			"%s forecast %.2f for readiness at %s could not produce a safe replica candidate. Recommendation unavailable: %s.",
			method,
			decision.Forecast.PredictedDemand,
			targetTime,
			codes,
		)
	}

	return fmt.Sprintf(
		"%s forecast %.2f for readiness at %s implied %d raw replicas and %d bounded replicas. Recommendation suppressed: %s.",
		method,
		decision.Forecast.PredictedDemand,
		targetTime,
		decision.Derived.RawRequiredReplicas,
		decision.Derived.FinalRecommendedReplicas,
		codes,
	)
}

// ReasonCodes extracts suppression reason codes in their existing order.
func ReasonCodes(reasons []SuppressionReason) []string {
	codes := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if reason.Code != "" {
			codes = append(codes, reason.Code)
		}
	}
	return codes
}

func buildDecisionForecastSummary(input BuildInput) ForecastSummary {
	if input.ForecastSummary != nil {
		summary := *input.ForecastSummary
		if summary.EvaluatedAt.IsZero() {
			summary.EvaluatedAt = input.EvaluationTime.UTC()
		}
		if summary.Method == "" {
			summary.Method = input.ForecastMethod
		}
		if summary.ForecastFor.IsZero() {
			summary.ForecastFor = input.ForecastTimestamp.UTC()
		}
		if summary.PredictedDemand == 0 && input.ForecastedDemand > 0 {
			summary.PredictedDemand = input.ForecastedDemand
		}
		if summary.Confidence == 0 && input.ConfidenceScore > 0 {
			summary.Confidence = input.ConfidenceScore
		}
		if summary.Message == "" {
			summary.Message = buildForecastMessage(summary)
		}
		return summary
	}

	summary := ForecastSummary{
		EvaluatedAt:     input.EvaluationTime.UTC(),
		Method:          input.ForecastMethod,
		ForecastFor:     input.ForecastTimestamp.UTC(),
		PredictedDemand: input.ForecastedDemand,
		Confidence:      input.ConfidenceScore,
	}
	summary.Message = buildForecastMessage(summary)
	return summary
}

func cloneTelemetrySummary(summary *TelemetryReadinessSummary) *TelemetryReadinessSummary {
	if summary == nil {
		return nil
	}

	signals := make([]TelemetrySignalSummary, 0, len(summary.Signals))
	signals = append(signals, summary.Signals...)

	return &TelemetryReadinessSummary{
		CheckedAt:       summary.CheckedAt,
		State:           summary.State,
		Message:         summary.Message,
		Reasons:         append([]string(nil), summary.Reasons...),
		BlockingReasons: append([]string(nil), summary.BlockingReasons...),
		Signals:         signals,
	}
}
