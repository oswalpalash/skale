package explain

import (
	"fmt"
	"strings"
	"time"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/metrics"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RecommendationExplanation is the stable explanation schema for one recommendation evaluation.
type RecommendationExplanation = Decision

// NodeHeadroomCheckResult is the stable explanation schema for the node headroom sanity check.
type NodeHeadroomCheckResult = NodeHeadroomAssessment

// WorkloadIdentity is the shared workload reference surfaced in explanations and status.
type WorkloadIdentity struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Resource  string `json:"resource,omitempty"`
	HPAName   string `json:"hpaName,omitempty"`
}

func (w WorkloadIdentity) IsZero() bool {
	return w.Namespace == "" && w.Name == "" && w.Kind == "" && w.Resource == "" && w.HPAName == ""
}

func (w WorkloadIdentity) DisplayName() string {
	switch {
	case w.Namespace != "" && w.Name != "":
		return w.Namespace + "/" + w.Name
	case w.Name != "":
		return w.Name
	case w.Resource != "":
		return w.Resource
	default:
		return ""
	}
}

// WorkloadIdentityFromString constructs the narrowest usable workload identity from a resource string.
func WorkloadIdentityFromString(resource string) WorkloadIdentity {
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return WorkloadIdentity{}
	}

	parts := strings.Split(resource, "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return WorkloadIdentity{
			Namespace: parts[0],
			Name:      parts[1],
			Resource:  resource,
		}
	}

	return WorkloadIdentity{
		Name:     resource,
		Resource: resource,
	}
}

// SignalSummary records the direct observed signals used to size or score an evaluation.
type SignalSummary struct {
	ObservedAt                  time.Time `json:"observedAt,omitempty"`
	CurrentDemand               float64   `json:"currentDemand,omitempty"`
	CurrentReplicas             int32     `json:"currentReplicas,omitempty"`
	TargetUtilization           float64   `json:"targetUtilization,omitempty"`
	EffectivePerReplicaCapacity float64   `json:"effectivePerReplicaCapacity,omitempty"`
	CapacityWindowStart         time.Time `json:"capacityWindowStart,omitempty"`
	CapacityWindowEnd           time.Time `json:"capacityWindowEnd,omitempty"`
	CapacitySampleCount         int       `json:"capacitySampleCount,omitempty"`
	WarmupSeconds               int64     `json:"warmupSeconds,omitempty"`
	RequiredReplicasProxy       *int32    `json:"requiredReplicasProxy,omitempty"`
}

// ForecastSummary is the shared operator-facing forecast digest used by recommendation and replay output.
type ForecastSummary struct {
	EvaluatedAt              time.Time `json:"evaluatedAt,omitempty"`
	GeneratedAt              time.Time `json:"generatedAt,omitempty"`
	Method                   string    `json:"method,omitempty"`
	HorizonSeconds           int64     `json:"horizonSeconds,omitempty"`
	SeasonalitySeconds       int64     `json:"seasonalitySeconds,omitempty"`
	SeasonalitySource        string    `json:"seasonalitySource,omitempty"`
	SeasonalityConfidence    float64   `json:"seasonalityConfidence,omitempty"`
	ForecastFor              time.Time `json:"forecastFor,omitempty"`
	PredictedDemand          float64   `json:"predictedDemand,omitempty"`
	Confidence               float64   `json:"confidence,omitempty"`
	Reliability              string    `json:"reliability,omitempty"`
	NormalizedError          float64   `json:"normalizedError,omitempty"`
	UnderPredictionRate      float64   `json:"underPredictionRate,omitempty"`
	MedianUnderPredictionPct float64   `json:"medianUnderPredictionPct,omitempty"`
	FallbackReason           string    `json:"fallbackReason,omitempty"`
	AdvisoryCodes            []string  `json:"advisoryCodes,omitempty"`
	AdvisoryMessages         []string  `json:"advisoryMessages,omitempty"`
	Message                  string    `json:"message,omitempty"`
	Error                    string    `json:"error,omitempty"`
}

// ForecastSummaryFromResult converts one selected forecast point into the shared explainability schema.
func ForecastSummaryFromResult(result forecast.Result, selectedPoint forecast.Point, evaluatedAt time.Time) ForecastSummary {
	method := strings.TrimSpace(result.Model)
	if method == "" {
		method = "forecast"
	}

	summary := ForecastSummary{
		EvaluatedAt:              evaluatedAt.UTC(),
		GeneratedAt:              result.GeneratedAt.UTC(),
		Method:                   method,
		HorizonSeconds:           int64(result.Horizon / time.Second),
		SeasonalitySeconds:       int64(result.Seasonality / time.Second),
		SeasonalitySource:        string(result.SeasonalitySource),
		SeasonalityConfidence:    result.SeasonalityConfidence,
		ForecastFor:              selectedPoint.Timestamp.UTC(),
		PredictedDemand:          selectedPoint.Value,
		Confidence:               result.Confidence,
		Reliability:              string(result.Reliability),
		NormalizedError:          result.Validation.NormalizedError,
		UnderPredictionRate:      result.Validation.UnderPredictionRate,
		MedianUnderPredictionPct: result.Validation.MedianUnderPredictionPct,
		FallbackReason:           result.FallbackReason,
		AdvisoryCodes:            advisoryCodes(result.Advisories),
		AdvisoryMessages:         advisoryMessages(result.Advisories),
	}
	summary.Message = buildForecastMessage(summary)
	return summary
}

// ForecastErrorSummary converts a forecast failure into the shared explainability schema.
func ForecastErrorSummary(model string, evaluatedAt time.Time, err error) ForecastSummary {
	method := strings.TrimSpace(model)
	if method == "" {
		method = "forecast"
	}

	summary := ForecastSummary{
		EvaluatedAt: evaluatedAt.UTC(),
		Method:      method,
	}
	if err != nil {
		summary.Error = err.Error()
		summary.Message = err.Error()
	}
	return summary
}

// TelemetrySignalSummary is the concise readiness view for one source signal.
type TelemetrySignalSummary struct {
	Name     string `json:"name,omitempty"`
	State    string `json:"state,omitempty"`
	Required bool   `json:"required,omitempty"`
	Message  string `json:"message,omitempty"`
}

// TelemetryReadinessSummary is the shared operator-facing telemetry readiness digest.
type TelemetryReadinessSummary struct {
	CheckedAt       time.Time                `json:"checkedAt,omitempty"`
	State           string                   `json:"state,omitempty"`
	Message         string                   `json:"message,omitempty"`
	Reasons         []string                 `json:"reasons,omitempty"`
	BlockingReasons []string                 `json:"blockingReasons,omitempty"`
	Signals         []TelemetrySignalSummary `json:"signals,omitempty"`
}

// TelemetrySummaryFromReadiness converts a detailed readiness report into a concise shared summary.
func TelemetrySummaryFromReadiness(report metrics.ReadinessReport) TelemetryReadinessSummary {
	signals := make([]TelemetrySignalSummary, 0, len(report.Signals))
	for _, signal := range report.Signals {
		signals = append(signals, TelemetrySignalSummary{
			Name:     string(signal.Name),
			State:    readinessSignalState(signal.Level),
			Required: signal.Required,
			Message:  signal.Message,
		})
	}

	return TelemetryReadinessSummary{
		CheckedAt:       report.CheckedAt.UTC(),
		State:           readinessState(report.Level),
		Message:         report.Summary,
		Reasons:         append([]string(nil), report.Reasons...),
		BlockingReasons: append([]string(nil), report.BlockingReasons...),
		Signals:         signals,
	}
}

// SuppressionExplanation is the shared explanation schema for a suppressed or unavailable recommendation.
type SuppressionExplanation struct {
	Workload      WorkloadIdentity    `json:"workload"`
	EvaluatedAt   time.Time           `json:"evaluatedAt,omitempty"`
	State         string              `json:"state,omitempty"`
	Signals       SignalSummary       `json:"signals"`
	Forecast      ForecastSummary     `json:"forecast"`
	BoundsApplied BoundsApplied       `json:"boundsApplied"`
	Reasons       []SuppressionReason `json:"reasons,omitempty"`
	Message       string              `json:"message,omitempty"`
}

// RecommendationSurface records the surfaced recommendation state for replay and status-oriented output.
type RecommendationSurface struct {
	State               string `json:"state,omitempty"`
	CurrentReplicas     int32  `json:"currentReplicas,omitempty"`
	RecommendedReplicas int32  `json:"recommendedReplicas,omitempty"`
	Delta               int32  `json:"delta,omitempty"`
	Message             string `json:"message,omitempty"`
}

// ReplayEventExplanation is the shared explanation schema for replay recommendation events.
type ReplayEventExplanation struct {
	Workload         WorkloadIdentity           `json:"workload"`
	EvaluatedAt      time.Time                  `json:"evaluatedAt,omitempty"`
	ActivationTime   *time.Time                 `json:"activationTime,omitempty"`
	BaselineReplicas int32                      `json:"baselineReplicas,omitempty"`
	ReplayReplicas   int32                      `json:"replayReplicas,omitempty"`
	BaselineOverload bool                       `json:"baselineOverload,omitempty"`
	ReplayOverload   bool                       `json:"replayOverload,omitempty"`
	BaselineExcess   bool                       `json:"baselineExcess,omitempty"`
	ReplayExcess     bool                       `json:"replayExcess,omitempty"`
	Signals          SignalSummary              `json:"signals"`
	Forecast         ForecastSummary            `json:"forecast"`
	Telemetry        *TelemetryReadinessSummary `json:"telemetry,omitempty"`
	Recommendation   RecommendationSurface      `json:"recommendation"`
	BoundsApplied    BoundsApplied              `json:"boundsApplied"`
	NodeHeadroom     *NodeHeadroomAssessment    `json:"nodeHeadroom,omitempty"`
	Suppression      *SuppressionExplanation    `json:"suppression,omitempty"`
	Summary          string                     `json:"summary,omitempty"`
}

// BuildSuppressionExplanation constructs the shared suppression schema from recommendation context.
func BuildSuppressionExplanation(
	workload WorkloadIdentity,
	evaluatedAt time.Time,
	signals SignalSummary,
	forecast ForecastSummary,
	bounds BoundsApplied,
	state string,
	reasons []SuppressionReason,
	message string,
) *SuppressionExplanation {
	if len(reasons) == 0 && strings.TrimSpace(message) == "" {
		return nil
	}

	explanation := &SuppressionExplanation{
		Workload:      workload,
		EvaluatedAt:   evaluatedAt.UTC(),
		State:         state,
		Signals:       signals,
		Forecast:      forecast,
		BoundsApplied: bounds,
		Reasons:       append([]SuppressionReason(nil), reasons...),
		Message:       strings.TrimSpace(message),
	}
	if explanation.Message == "" {
		explanation.Message = buildSuppressionMessage(state, reasons)
	}
	return explanation
}

// StatusProjection renders the shared explainability schema into CRD status-friendly shapes.
type StatusProjection struct{}

func (StatusProjection) ObservedWorkload(identity WorkloadIdentity) *skalev1alpha1.ObservedWorkloadIdentity {
	if identity.IsZero() {
		return nil
	}
	return &skalev1alpha1.ObservedWorkloadIdentity{
		Kind:      identity.Kind,
		Namespace: identity.Namespace,
		Name:      identity.Name,
		HPAName:   identity.HPAName,
	}
}

func (StatusProjection) Telemetry(summary TelemetryReadinessSummary) *skalev1alpha1.TelemetryReadinessSummary {
	if summary.CheckedAt.IsZero() && summary.State == "" && summary.Message == "" && len(summary.Signals) == 0 {
		return nil
	}

	signals := make([]skalev1alpha1.SignalHealth, 0, len(summary.Signals))
	for _, signal := range summary.Signals {
		signals = append(signals, skalev1alpha1.SignalHealth{
			Name:    signal.Name,
			State:   statusSignalHealthState(signal.State),
			Message: signal.Message,
		})
	}

	return &skalev1alpha1.TelemetryReadinessSummary{
		State:     statusTelemetryReadinessState(summary.State),
		CheckedAt: timeToMetaPtr(summary.CheckedAt),
		Message:   summary.Message,
		Signals:   signals,
	}
}

func (StatusProjection) Forecast(summary ForecastSummary) *skalev1alpha1.ForecastSummary {
	if summary.EvaluatedAt.IsZero() && summary.Method == "" && summary.Message == "" {
		return nil
	}
	return &skalev1alpha1.ForecastSummary{
		EvaluatedAt:              timeToMetaPtr(summary.EvaluatedAt),
		Method:                   summary.Method,
		Horizon:                  metaDuration(summary.HorizonSeconds),
		Seasonality:              metaDuration(summary.SeasonalitySeconds),
		SeasonalitySource:        summary.SeasonalitySource,
		SeasonalityConfidence:    summary.SeasonalityConfidence,
		Confidence:               summary.Confidence,
		UnderPredictionRate:      summary.UnderPredictionRate,
		MedianUnderPredictionPct: summary.MedianUnderPredictionPct,
		Message:                  summary.Message,
	}
}

func (StatusProjection) Recommendation(decision RecommendationExplanation) *skalev1alpha1.RecommendationSummary {
	if decision.EvaluationTime.IsZero() && decision.Outcome.State == "" && decision.Outcome.Message == "" {
		return nil
	}
	return &skalev1alpha1.RecommendationSummary{
		EvaluatedAt:         timeToMetaPtr(decision.EvaluationTime),
		State:               skalev1alpha1.RecommendationState(decision.Outcome.State),
		BaselineReplicas:    decision.Inputs.CurrentReplicas,
		RecommendedReplicas: decision.Outcome.FinalRecommendedReplicas,
		BoundedReplicas:     decision.Derived.StepBoundReplicas,
		Message:             decision.Outcome.Message,
	}
}

func (StatusProjection) SuppressionReasons(reasons []SuppressionReason) []skalev1alpha1.SuppressionReason {
	if len(reasons) == 0 {
		return nil
	}
	out := make([]skalev1alpha1.SuppressionReason, 0, len(reasons))
	for _, reason := range reasons {
		out = append(out, skalev1alpha1.SuppressionReason{
			Code:    reason.Code,
			Message: reason.Message,
		})
	}
	return out
}

func buildForecastMessage(summary ForecastSummary) string {
	if summary.Error != "" {
		return summary.Error
	}
	method := strings.TrimSpace(summary.Method)
	if method == "" {
		method = "forecast"
	}
	seasonality := forecastSeasonalityClause(summary)
	if summary.ForecastFor.IsZero() {
		return fmt.Sprintf("%s predicted %.2f demand%s", method, summary.PredictedDemand, seasonality)
	}
	return fmt.Sprintf(
		"%s predicted %.2f demand for %s at confidence %.2f%s",
		method,
		summary.PredictedDemand,
		summary.ForecastFor.UTC().Format(time.RFC3339),
		summary.Confidence,
		seasonality,
	)
}

func forecastSeasonalityClause(summary ForecastSummary) string {
	switch summary.SeasonalitySource {
	case string(forecast.SeasonalitySourceConfigured):
		if summary.SeasonalitySeconds > 0 {
			return fmt.Sprintf(" using configured seasonality %s", time.Duration(summary.SeasonalitySeconds)*time.Second)
		}
	case string(forecast.SeasonalitySourceDetected):
		if summary.SeasonalitySeconds > 0 {
			return fmt.Sprintf(" using detected seasonality %s at confidence %.2f", time.Duration(summary.SeasonalitySeconds)*time.Second, summary.SeasonalityConfidence)
		}
	case string(forecast.SeasonalitySourceNone):
		return " with no seasonality detected"
	}
	return ""
}

func buildSuppressionMessage(state string, reasons []SuppressionReason) string {
	codes := ReasonCodes(reasons)
	if len(codes) == 0 {
		if strings.TrimSpace(state) == "" {
			return "recommendation was not surfaced"
		}
		return fmt.Sprintf("recommendation entered %s state", state)
	}
	return fmt.Sprintf("recommendation %s: %s", state, strings.Join(codes, ", "))
}

func advisoryCodes(advisories []forecast.Advisory) []string {
	out := make([]string, 0, len(advisories))
	for _, advisory := range advisories {
		if advisory.Code != "" {
			out = append(out, advisory.Code)
		}
	}
	return out
}

func advisoryMessages(advisories []forecast.Advisory) []string {
	out := make([]string, 0, len(advisories))
	for _, advisory := range advisories {
		if advisory.Message != "" {
			out = append(out, advisory.Message)
		}
	}
	return out
}

func readinessState(level metrics.ReadinessLevel) string {
	switch level {
	case metrics.ReadinessLevelSupported:
		return "ready"
	case metrics.ReadinessLevelDegraded:
		return "degraded"
	default:
		return "unsupported"
	}
}

func readinessSignalState(level metrics.SignalLevel) string {
	switch level {
	case metrics.SignalLevelSupported:
		return "ready"
	case metrics.SignalLevelDegraded:
		return "degraded"
	case metrics.SignalLevelMissing:
		return "missing"
	default:
		return "unsupported"
	}
}

func statusTelemetryReadinessState(state string) skalev1alpha1.TelemetryReadinessState {
	switch state {
	case "ready":
		return skalev1alpha1.TelemetryReadinessStateReady
	case "degraded":
		return skalev1alpha1.TelemetryReadinessStateDegraded
	default:
		return skalev1alpha1.TelemetryReadinessStateUnsupported
	}
}

func statusSignalHealthState(state string) skalev1alpha1.SignalHealthState {
	switch state {
	case "ready":
		return skalev1alpha1.SignalHealthStateReady
	case "degraded":
		return skalev1alpha1.SignalHealthStateDegraded
	default:
		return skalev1alpha1.SignalHealthStateMissing
	}
}

func timeToMetaPtr(value time.Time) *metav1.Time {
	if value.IsZero() {
		return nil
	}
	meta := metav1.NewTime(value.UTC())
	return &meta
}

func metaDuration(seconds int64) metav1.Duration {
	return metav1.Duration{Duration: time.Duration(seconds) * time.Second}
}
