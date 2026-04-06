package safety

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/oswalpalash/skale/internal/explain"
)

var ErrInvalidInput = errors.New("invalid safety input")

type NodeHeadroomMode string

const (
	NodeHeadroomModeDisabled          NodeHeadroomMode = "disabled"
	NodeHeadroomModeRequireForScaleUp NodeHeadroomMode = "requireForScaleUp"
)

type NodeHeadroomState string

const (
	NodeHeadroomStateReady       NodeHeadroomState = "ready"
	NodeHeadroomStateMissing     NodeHeadroomState = "missing"
	NodeHeadroomStateStale       NodeHeadroomState = "stale"
	NodeHeadroomStateUnsupported NodeHeadroomState = "unsupported"
)

type OperatorMode string

const (
	OperatorModeEnabled  OperatorMode = "enabled"
	OperatorModePaused   OperatorMode = "paused"
	OperatorModeDisabled OperatorMode = "disabled"
)

type TelemetryLevel string

const (
	TelemetryLevelReady       TelemetryLevel = "ready"
	TelemetryLevelDegraded    TelemetryLevel = "degraded"
	TelemetryLevelUnsupported TelemetryLevel = "unsupported"
)

// PreviousRecommendation captures the last surfaced replica recommendation.
type PreviousRecommendation struct {
	RecommendedReplicas int32
	ChangedAt           time.Time
}

// TelemetryStatus summarizes whether signal quality is good enough for safe recommendation surfacing.
type TelemetryStatus struct {
	Level   TelemetryLevel
	Message string
	Reasons []string
}

// ModelDivergenceStatus summarizes disagreement between candidate forecast models.
type ModelDivergenceStatus struct {
	Divergence     float64
	MaximumAllowed float64
}

// ForecastErrorStatus summarizes recent forecast quality in normalized error units.
type ForecastErrorStatus struct {
	NormalizedError float64
	MaximumAllowed  float64
}

// BlackoutWindow suppresses recommendation surfacing during a bounded time range.
type BlackoutWindow struct {
	Name   string
	Start  time.Time
	End    time.Time
	Reason string
}

// DependencyHealthStatus reports whether an external dependency is healthy enough to support additional load.
type DependencyHealthStatus struct {
	Name                string
	Healthy             bool
	HealthyRatio        float64
	MinimumHealthyRatio float64
	Message             string
}

// CircuitBreaker opens when recent recommendation quality is poor enough that the system should fail closed.
type CircuitBreaker struct {
	ConsecutivePoorEvaluations    int
	MaxConsecutivePoorEvaluations int
	PoorEvaluationFraction        float64
	MaxPoorEvaluationFraction     float64
	WindowSize                    int
}

// Open returns whether the fail-closed circuit breaker should suppress new recommendations.
func (c CircuitBreaker) Open() bool {
	if c.MaxConsecutivePoorEvaluations > 0 && c.ConsecutivePoorEvaluations >= c.MaxConsecutivePoorEvaluations {
		return true
	}
	if c.WindowSize > 0 && c.MaxPoorEvaluationFraction > 0 && c.PoorEvaluationFraction >= c.MaxPoorEvaluationFraction {
		return true
	}
	return false
}

func (c CircuitBreaker) reason() string {
	if c.MaxConsecutivePoorEvaluations > 0 && c.ConsecutivePoorEvaluations >= c.MaxConsecutivePoorEvaluations {
		return fmt.Sprintf(
			"recent recommendation quality opened the circuit breaker after %d consecutive poor evaluations",
			c.ConsecutivePoorEvaluations,
		)
	}
	if c.WindowSize > 0 && c.MaxPoorEvaluationFraction > 0 && c.PoorEvaluationFraction >= c.MaxPoorEvaluationFraction {
		return fmt.Sprintf(
			"recent recommendation quality opened the circuit breaker with %.0f%% poor evaluations across the last %d samples",
			c.PoorEvaluationFraction*100,
			c.WindowSize,
		)
	}
	return "recent recommendation quality opened the circuit breaker"
}

// Input captures the post-forecast context checked by v1 safety policy.
//
// The safety evaluator owns:
// - replica bounds
// - confidence gating
// - readiness and quality suppression
// - operator and policy suppression
// - cluster headroom sanity
type Input struct {
	EvaluationTime      time.Time
	CurrentReplicas     int32
	RawProposedReplicas int32

	MinReplicas int32
	MaxReplicas int32
	MaxStepUp   *int32
	MaxStepDown *int32

	ConfidenceScore     float64
	ConfidenceThreshold float64

	Telemetry        *TelemetryStatus
	ModelDivergence  *ModelDivergenceStatus
	ForecastError    *ForecastErrorStatus
	BlackoutWindows  []BlackoutWindow
	DependencyHealth []DependencyHealthStatus

	CooldownWindow     time.Duration
	LastRecommendation *PreviousRecommendation
	NodeHeadroomMode   NodeHeadroomMode
	NodeHeadroom       *NodeHeadroomSignal
	OperatorMode       OperatorMode
	CircuitBreaker     *CircuitBreaker
}

// CheckStatus records the status of each safety gate.
type CheckStatus struct {
	ConfidencePassed          bool
	TelemetryReady            *bool
	ModelDivergencePassed     *bool
	RecentForecastErrorPassed *bool
	BlackoutPassed            bool
	DependencyHealthPassed    *bool
	CooldownPassed            bool
	NodeHeadroomStatus        *HeadroomStatus
	NodeHeadroomPassed        *bool
	OperatorMode              OperatorMode
	OperatorEnabled           bool
	CircuitBreakerClosed      *bool
}

// Result records the safety outcome for a proposed recommendation.
type Result struct {
	PolicyBoundReplicas   int32
	StepBoundReplicas     int32
	FinalProposedReplicas int32

	MinMaxBounded   bool
	StepUpBounded   bool
	StepDownBounded bool
	BoundDetails    []explain.BoundDetail

	Suppressed   bool
	Reasons      []explain.SuppressionReason
	Checks       CheckStatus
	NodeHeadroom *NodeHeadroomAssessment
}

// Evaluator applies v1 bounds and fail-closed policy checks.
type Evaluator interface {
	Evaluate(input Input) (Result, error)
}

// DefaultEvaluator implements the default v1 bounds and suppression policy.
type DefaultEvaluator struct {
	HeadroomEstimator NodeHeadroomEstimator
}

func (e DefaultEvaluator) Evaluate(input Input) (Result, error) {
	if err := input.Validate(); err != nil {
		return Result{}, err
	}

	result := Result{
		Checks: CheckStatus{
			ConfidencePassed: input.ConfidenceScore >= input.ConfidenceThreshold,
			BlackoutPassed:   true,
			CooldownPassed:   true,
			OperatorMode:     normalizedOperatorMode(input.OperatorMode),
		},
	}
	result.Checks.OperatorEnabled = result.Checks.OperatorMode == OperatorModeEnabled

	result.PolicyBoundReplicas, result.StepBoundReplicas, result.FinalProposedReplicas,
		result.MinMaxBounded, result.StepUpBounded, result.StepDownBounded, result.BoundDetails = applyBounds(
		input.RawProposedReplicas,
		input.CurrentReplicas,
		input.MinReplicas,
		input.MaxReplicas,
		input.MaxStepUp,
		input.MaxStepDown,
	)

	if !result.Checks.OperatorEnabled {
		if result.Checks.OperatorMode == OperatorModePaused {
			result.Reasons = append(result.Reasons, suppressionReason(
				explain.ReasonOperatorPaused,
				explain.SuppressionCategoryOperator,
				explain.SuppressionSeverityWarning,
				"recommendation surfacing is paused by operator policy",
			))
		} else {
			result.Reasons = append(result.Reasons, suppressionReason(
				explain.ReasonOperatorDisabled,
				explain.SuppressionCategoryOperator,
				explain.SuppressionSeverityError,
				"recommendation surfacing is disabled by operator policy",
			))
		}
	}

	if input.Telemetry != nil {
		ready := input.Telemetry.Level == TelemetryLevelReady
		result.Checks.TelemetryReady = boolPtr(ready)
		if !ready {
			message := fmt.Sprintf("telemetry readiness is %s", input.Telemetry.Level)
			details := telemetryMessage(*input.Telemetry)
			if details != "" {
				message = message + ": " + details
			}
			result.Reasons = append(result.Reasons, suppressionReason(
				explain.ReasonTelemetryNotReady,
				explain.SuppressionCategoryTelemetry,
				explain.SuppressionSeverityError,
				message,
			))
		}
	}

	if !result.Checks.ConfidencePassed {
		result.Reasons = append(result.Reasons, suppressionReason(
			explain.ReasonLowConfidence,
			explain.SuppressionCategoryForecast,
			explain.SuppressionSeverityWarning,
			fmt.Sprintf("confidence %.2f is below threshold %.2f", input.ConfidenceScore, input.ConfidenceThreshold),
		))
	}

	if input.ModelDivergence != nil {
		passed := input.ModelDivergence.Divergence <= input.ModelDivergence.MaximumAllowed
		result.Checks.ModelDivergencePassed = boolPtr(passed)
		if !passed {
			result.Reasons = append(result.Reasons, suppressionReason(
				explain.ReasonModelDivergenceTooHigh,
				explain.SuppressionCategoryModel,
				explain.SuppressionSeverityWarning,
				fmt.Sprintf(
					"model divergence %.2f exceeds threshold %.2f",
					input.ModelDivergence.Divergence,
					input.ModelDivergence.MaximumAllowed,
				),
			))
		}
	}

	if input.ForecastError != nil {
		passed := input.ForecastError.NormalizedError <= input.ForecastError.MaximumAllowed
		result.Checks.RecentForecastErrorPassed = boolPtr(passed)
		if !passed {
			result.Reasons = append(result.Reasons, suppressionReason(
				explain.ReasonForecastErrorTooHigh,
				explain.SuppressionCategoryModel,
				explain.SuppressionSeverityWarning,
				fmt.Sprintf(
					"recent normalized forecast error %.2f exceeds threshold %.2f",
					input.ForecastError.NormalizedError,
					input.ForecastError.MaximumAllowed,
				),
			))
		}
	}

	if input.CircuitBreaker != nil {
		closed := !input.CircuitBreaker.Open()
		result.Checks.CircuitBreakerClosed = boolPtr(closed)
		if !closed {
			result.Reasons = append(result.Reasons, suppressionReason(
				explain.ReasonCircuitBreakerOpen,
				explain.SuppressionCategoryPolicy,
				explain.SuppressionSeverityError,
				input.CircuitBreaker.reason(),
			))
		}
	}

	if window := activeBlackoutWindow(input.EvaluationTime, input.BlackoutWindows); window != nil {
		result.Checks.BlackoutPassed = false
		message := fmt.Sprintf("blackout window %q is active", window.Name)
		if window.Reason != "" {
			message = message + ": " + window.Reason
		}
		result.Reasons = append(result.Reasons, suppressionReason(
			explain.ReasonBlackoutWindowActive,
			explain.SuppressionCategoryPolicy,
			explain.SuppressionSeverityWarning,
			message,
		))
	}

	if len(input.DependencyHealth) > 0 {
		passed := true
		for _, dependency := range input.DependencyHealth {
			if dependencyHealthy(dependency) {
				continue
			}
			passed = false
			result.Reasons = append(result.Reasons, suppressionReason(
				explain.ReasonDependencyHealthFailed,
				explain.SuppressionCategoryDependency,
				explain.SuppressionSeverityError,
				dependencyFailureMessage(dependency),
			))
		}
		result.Checks.DependencyHealthPassed = boolPtr(passed)
	}

	if input.CooldownWindow > 0 &&
		input.LastRecommendation != nil &&
		!input.LastRecommendation.ChangedAt.IsZero() &&
		result.FinalProposedReplicas != input.LastRecommendation.RecommendedReplicas &&
		input.EvaluationTime.Before(input.LastRecommendation.ChangedAt.Add(input.CooldownWindow)) {
		result.Checks.CooldownPassed = false
		result.Reasons = append(result.Reasons, suppressionReason(
			explain.ReasonCooldownActive,
			explain.SuppressionCategoryStabilization,
			explain.SuppressionSeverityWarning,
			fmt.Sprintf("cooldown window remains active until %s", input.LastRecommendation.ChangedAt.Add(input.CooldownWindow).UTC().Format(time.RFC3339)),
		))
	}

	if input.NodeHeadroomMode == NodeHeadroomModeRequireForScaleUp && result.FinalProposedReplicas > input.CurrentReplicas {
		delta := result.FinalProposedReplicas - input.CurrentReplicas
		estimator := e.HeadroomEstimator
		if estimator == nil {
			estimator = ConservativeNodeHeadroomEstimator{}
		}
		assessment, err := estimator.Assess(input.NodeHeadroom, delta)
		if err != nil {
			return Result{}, err
		}
		result.NodeHeadroom = &assessment
		result.Checks.NodeHeadroomStatus = headroomStatusPtr(assessment.Status)
		headroomPassed := assessment.Status == HeadroomStatusSufficient
		result.Checks.NodeHeadroomPassed = boolPtr(headroomPassed)
		if !headroomPassed {
			result.Reasons = append(result.Reasons, headroomSuppressionReason(input.NodeHeadroom, assessment))
		}
	}

	result.Reasons = dedupeReasons(result.Reasons)
	result.Suppressed = len(result.Reasons) > 0
	return result, nil
}

// Validate checks the input invariants for the v1 safety evaluator.
func (i Input) Validate() error {
	switch normalizedOperatorMode(i.OperatorMode) {
	case OperatorModeEnabled, OperatorModePaused, OperatorModeDisabled:
	default:
		return fmt.Errorf("%w: unsupported operator mode %q", ErrInvalidInput, i.OperatorMode)
	}
	switch i.NodeHeadroomMode {
	case "", NodeHeadroomModeDisabled, NodeHeadroomModeRequireForScaleUp:
	default:
		return fmt.Errorf("%w: unsupported node headroom mode %q", ErrInvalidInput, i.NodeHeadroomMode)
	}
	if i.EvaluationTime.IsZero() {
		return fmt.Errorf("%w: evaluation time is required", ErrInvalidInput)
	}
	if i.CurrentReplicas < 0 {
		return fmt.Errorf("%w: current replicas must be non-negative", ErrInvalidInput)
	}
	if i.RawProposedReplicas < 0 {
		return fmt.Errorf("%w: raw proposed replicas must be non-negative", ErrInvalidInput)
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
	if i.ConfidenceScore < 0 || i.ConfidenceScore > 1 {
		return fmt.Errorf("%w: confidence score must be in the range [0, 1]", ErrInvalidInput)
	}
	if i.ConfidenceThreshold < 0 || i.ConfidenceThreshold > 1 {
		return fmt.Errorf("%w: confidence threshold must be in the range [0, 1]", ErrInvalidInput)
	}
	if i.CooldownWindow < 0 {
		return fmt.Errorf("%w: cooldown window must be non-negative", ErrInvalidInput)
	}
	if i.LastRecommendation != nil && i.LastRecommendation.RecommendedReplicas < 0 {
		return fmt.Errorf("%w: last recommendation replicas must be non-negative", ErrInvalidInput)
	}
	if i.NodeHeadroom != nil {
		if err := i.NodeHeadroom.Validate(); err != nil {
			return err
		}
	}
	if i.Telemetry != nil {
		switch i.Telemetry.Level {
		case TelemetryLevelReady, TelemetryLevelDegraded, TelemetryLevelUnsupported:
		default:
			return fmt.Errorf("%w: unsupported telemetry level %q", ErrInvalidInput, i.Telemetry.Level)
		}
	}
	if i.ModelDivergence != nil {
		if i.ModelDivergence.Divergence < 0 {
			return fmt.Errorf("%w: model divergence must be non-negative", ErrInvalidInput)
		}
		if i.ModelDivergence.MaximumAllowed <= 0 {
			return fmt.Errorf("%w: model divergence threshold must be positive", ErrInvalidInput)
		}
	}
	if i.ForecastError != nil {
		if i.ForecastError.NormalizedError < 0 {
			return fmt.Errorf("%w: normalized forecast error must be non-negative", ErrInvalidInput)
		}
		if i.ForecastError.MaximumAllowed <= 0 {
			return fmt.Errorf("%w: normalized forecast error threshold must be positive", ErrInvalidInput)
		}
	}
	for _, window := range i.BlackoutWindows {
		if window.Name == "" {
			return fmt.Errorf("%w: blackout window name is required", ErrInvalidInput)
		}
		if window.Start.IsZero() || window.End.IsZero() || !window.Start.Before(window.End) {
			return fmt.Errorf("%w: blackout window %q must have start before end", ErrInvalidInput, window.Name)
		}
	}
	for _, dependency := range i.DependencyHealth {
		if dependency.Name == "" {
			return fmt.Errorf("%w: dependency name is required", ErrInvalidInput)
		}
		if dependency.HealthyRatio < 0 || dependency.HealthyRatio > 1 {
			return fmt.Errorf("%w: dependency %q healthy ratio must be in the range [0, 1]", ErrInvalidInput, dependency.Name)
		}
		if dependency.MinimumHealthyRatio < 0 || dependency.MinimumHealthyRatio > 1 {
			return fmt.Errorf("%w: dependency %q minimum healthy ratio must be in the range [0, 1]", ErrInvalidInput, dependency.Name)
		}
	}
	if i.CircuitBreaker != nil {
		if i.CircuitBreaker.ConsecutivePoorEvaluations < 0 {
			return fmt.Errorf("%w: consecutive poor evaluations must be non-negative", ErrInvalidInput)
		}
		if i.CircuitBreaker.MaxConsecutivePoorEvaluations < 0 {
			return fmt.Errorf("%w: max consecutive poor evaluations must be non-negative", ErrInvalidInput)
		}
		if i.CircuitBreaker.PoorEvaluationFraction < 0 || i.CircuitBreaker.PoorEvaluationFraction > 1 {
			return fmt.Errorf("%w: poor evaluation fraction must be in the range [0, 1]", ErrInvalidInput)
		}
		if i.CircuitBreaker.MaxPoorEvaluationFraction < 0 || i.CircuitBreaker.MaxPoorEvaluationFraction > 1 {
			return fmt.Errorf("%w: max poor evaluation fraction must be in the range [0, 1]", ErrInvalidInput)
		}
		if i.CircuitBreaker.WindowSize < 0 {
			return fmt.Errorf("%w: circuit breaker window size must be non-negative", ErrInvalidInput)
		}
	}
	return nil
}

func applyBounds(
	raw int32,
	current int32,
	minReplicas int32,
	maxReplicas int32,
	maxStepUp *int32,
	maxStepDown *int32,
) (policyBound int32, stepBound int32, final int32, minMaxBounded bool, stepUpBounded bool, stepDownBounded bool, details []explain.BoundDetail) {
	policyBound = clampReplicas(raw, minReplicas, maxReplicas)
	final = applyStepBounds(policyBound, current, minReplicas, maxReplicas, maxStepUp, maxStepDown)
	stepBound = final

	if raw < minReplicas {
		minMaxBounded = true
		details = append(details, explain.BoundDetail{
			Code:    "min_replicas",
			Message: fmt.Sprintf("raw recommendation %d was clamped to min replicas %d", raw, minReplicas),
		})
	} else if raw > maxReplicas {
		minMaxBounded = true
		details = append(details, explain.BoundDetail{
			Code:    "max_replicas",
			Message: fmt.Sprintf("raw recommendation %d was clamped to max replicas %d", raw, maxReplicas),
		})
	} else if current < minReplicas {
		minMaxBounded = true
		details = append(details, explain.BoundDetail{
			Code:    "current_below_min",
			Message: fmt.Sprintf("current replicas %d are below min replicas %d; forcing bounded recommendation to %d", current, minReplicas, minReplicas),
		})
	} else if current > maxReplicas {
		minMaxBounded = true
		details = append(details, explain.BoundDetail{
			Code:    "current_above_max",
			Message: fmt.Sprintf("current replicas %d are above max replicas %d; forcing bounded recommendation to %d", current, maxReplicas, maxReplicas),
		})
	}

	if current >= minReplicas && current <= maxReplicas && policyBound > current && final < policyBound {
		stepUpBounded = true
		limit := maxReplicas
		if maxStepUp != nil {
			limit = saturatingAdd(current, *maxStepUp)
		}
		details = append(details, explain.BoundDetail{
			Code:    "step_up",
			Message: fmt.Sprintf("bounded recommendation %d was limited by step-up policy to %d", policyBound, limit),
		})
	}

	if current >= minReplicas && current <= maxReplicas && policyBound < current && final > policyBound {
		stepDownBounded = true
		limit := minReplicas
		if maxStepDown != nil {
			limit = saturatingSub(current, *maxStepDown)
		}
		details = append(details, explain.BoundDetail{
			Code:    "step_down",
			Message: fmt.Sprintf("bounded recommendation %d was limited by step-down policy to %d", policyBound, limit),
		})
	}

	return policyBound, stepBound, final, minMaxBounded, stepUpBounded, stepDownBounded, details
}

func applyStepBounds(target, current, minReplicas, maxReplicas int32, maxStepUp, maxStepDown *int32) int32 {
	if current < minReplicas {
		return minReplicas
	}
	if current > maxReplicas {
		return maxReplicas
	}

	final := target
	if target > current && maxStepUp != nil {
		maxAllowed := saturatingAdd(current, *maxStepUp)
		if final > maxAllowed {
			final = maxAllowed
		}
	}
	if target < current && maxStepDown != nil {
		minAllowed := saturatingSub(current, *maxStepDown)
		if final < minAllowed {
			final = minAllowed
		}
	}

	return clampReplicas(final, minReplicas, maxReplicas)
}

func clampReplicas(value, minValue, maxValue int32) int32 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func saturatingAdd(a, b int32) int32 {
	sum := int64(a) + int64(b)
	if sum > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(sum)
}

func saturatingSub(a, b int32) int32 {
	diff := int64(a) - int64(b)
	if diff < math.MinInt32 {
		return math.MinInt32
	}
	return int32(diff)
}

func normalizedOperatorMode(mode OperatorMode) OperatorMode {
	if mode == "" {
		return OperatorModeEnabled
	}
	return mode
}

func activeBlackoutWindow(evaluatedAt time.Time, windows []BlackoutWindow) *BlackoutWindow {
	for index := range windows {
		window := windows[index]
		if (evaluatedAt.Equal(window.Start) || evaluatedAt.After(window.Start)) &&
			(evaluatedAt.Equal(window.End) || evaluatedAt.Before(window.End)) {
			return &window
		}
	}
	return nil
}

func dependencyHealthy(dependency DependencyHealthStatus) bool {
	if !dependency.Healthy {
		return false
	}
	if dependency.MinimumHealthyRatio > 0 && dependency.HealthyRatio < dependency.MinimumHealthyRatio {
		return false
	}
	return true
}

func dependencyFailureMessage(dependency DependencyHealthStatus) string {
	if dependency.Message != "" {
		return dependency.Message
	}
	if dependency.MinimumHealthyRatio > 0 {
		return fmt.Sprintf(
			"dependency %q healthy ratio %.2f is below minimum %.2f",
			dependency.Name,
			dependency.HealthyRatio,
			dependency.MinimumHealthyRatio,
		)
	}
	return fmt.Sprintf("dependency %q is not healthy enough for additional load", dependency.Name)
}

func telemetryMessage(status TelemetryStatus) string {
	if len(status.Reasons) > 0 {
		return strings.Join(status.Reasons, "; ")
	}
	return status.Message
}

func boolPtr(value bool) *bool {
	return &value
}

func headroomStatusPtr(value HeadroomStatus) *HeadroomStatus {
	return &value
}

func suppressionReason(code string, category explain.SuppressionCategory, severity explain.SuppressionSeverity, message string) explain.SuppressionReason {
	return explain.SuppressionReason{
		Code:     code,
		Category: category,
		Severity: severity,
		Message:  message,
	}
}

func headroomSuppressionReason(signal *NodeHeadroomSignal, assessment NodeHeadroomAssessment) explain.SuppressionReason {
	if assessment.Status == HeadroomStatusInsufficient {
		return suppressionReason(
			explain.ReasonInsufficientNodeHeadroom,
			explain.SuppressionCategoryCluster,
			explain.SuppressionSeverityError,
			assessment.Message,
		)
	}

	switch {
	case signal == nil || normalizedNodeHeadroomState(signal.State) == NodeHeadroomStateMissing:
		return suppressionReason(
			explain.ReasonMissingNodeHeadroom,
			explain.SuppressionCategoryCluster,
			explain.SuppressionSeverityError,
			assessment.Message,
		)
	case normalizedNodeHeadroomState(signal.State) == NodeHeadroomStateStale:
		return suppressionReason(
			explain.ReasonStaleNodeHeadroom,
			explain.SuppressionCategoryCluster,
			explain.SuppressionSeverityError,
			assessment.Message,
		)
	case normalizedNodeHeadroomState(signal.State) == NodeHeadroomStateUnsupported:
		return suppressionReason(
			explain.ReasonUnsupportedNodeHeadroom,
			explain.SuppressionCategoryCluster,
			explain.SuppressionSeverityError,
			assessment.Message,
		)
	default:
		return suppressionReason(
			explain.ReasonUncertainNodeHeadroom,
			explain.SuppressionCategoryCluster,
			explain.SuppressionSeverityWarning,
			assessment.Message,
		)
	}
}

func dedupeReasons(reasons []explain.SuppressionReason) []explain.SuppressionReason {
	seen := make(map[string]struct{}, len(reasons))
	deduped := make([]explain.SuppressionReason, 0, len(reasons))
	for _, reason := range reasons {
		key := reason.Code + "\x00" + string(reason.Category) + "\x00" + string(reason.Severity) + "\x00" + reason.Message
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, reason)
	}
	return deduped
}
