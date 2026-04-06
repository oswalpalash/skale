package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type PredictiveScalingMode string

const (
	PredictiveScalingModeRecommendationOnly PredictiveScalingMode = "recommendationOnly"
)

type NodeHeadroomSanityMode string

const (
	NodeHeadroomSanityDisabled          NodeHeadroomSanityMode = "disabled"
	NodeHeadroomSanityRequireForScaleUp NodeHeadroomSanityMode = "requireForScaleUp"
)

type DependencyHealthCheckType string

const (
	DependencyHealthCheckTypePrometheusQuery DependencyHealthCheckType = "prometheusQuery"
)

type TelemetryReadinessState string

const (
	TelemetryReadinessStateReady       TelemetryReadinessState = "ready"
	TelemetryReadinessStateDegraded    TelemetryReadinessState = "degraded"
	TelemetryReadinessStateUnsupported TelemetryReadinessState = "unsupported"
)

type SignalHealthState string

const (
	SignalHealthStateReady    SignalHealthState = "ready"
	SignalHealthStateDegraded SignalHealthState = "degraded"
	SignalHealthStateMissing  SignalHealthState = "missing"
)

type RecommendationState string

const (
	RecommendationStateAvailable   RecommendationState = "available"
	RecommendationStateSuppressed  RecommendationState = "suppressed"
	RecommendationStateUnavailable RecommendationState = "unavailable"
)

// TargetReference selects the supported workload under evaluation.
// The target is assumed to live in the same namespace as the policy.
type TargetReference struct {
	// +kubebuilder:default:=apps/v1
	// +kubebuilder:validation:Enum=apps/v1
	APIVersion string `json:"apiVersion,omitempty"`

	// +kubebuilder:default:=Deployment
	// +kubebuilder:validation:Enum=Deployment
	Kind string `json:"kind,omitempty"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// WarmupSettings captures the minimum warmup input needed for bounded recommendations.
type WarmupSettings struct {
	// EstimatedReadyDuration is the typical time for a new replica to become useful.
	// +kubebuilder:default:="45s"
	EstimatedReadyDuration metav1.Duration `json:"estimatedReadyDuration,omitempty"`
}

// ScaleStepPolicy bounds how much a recommendation may change in one evaluation.
type ScaleStepPolicy struct {
	// +kubebuilder:validation:Minimum=1
	MaxReplicasChange int32 `json:"maxReplicasChange"`
}

// BlackoutWindow disables recommendation surfacing during a bounded time range.
type BlackoutWindow struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	Start metav1.Time `json:"start"`
	End   metav1.Time `json:"end"`

	// +optional
	// +kubebuilder:validation:MaxLength=256
	Reason string `json:"reason,omitempty"`
}

// KnownEvent marks an operator-supplied window that may influence replay and evaluation later.
type KnownEvent struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	Start metav1.Time `json:"start"`
	End   metav1.Time `json:"end"`

	// +optional
	// +kubebuilder:validation:MaxLength=256
	Note string `json:"note,omitempty"`
}

// DependencyHealthCheck defines a small v1 contract for gating recommendations on external health.
type DependencyHealthCheck struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// +kubebuilder:default:=prometheusQuery
	// +kubebuilder:validation:Enum=prometheusQuery
	Type DependencyHealthCheckType `json:"type,omitempty"`

	// Query is expected to resolve to a 0..1 healthy ratio in v1.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	Query string `json:"query"`

	// +kubebuilder:validation:Minimum=0.01
	// +kubebuilder:validation:Maximum=1
	MinHealthyRatio float64 `json:"minHealthyRatio"`
}

// PredictiveScalingPolicySpec declares the desired recommendation-only policy surface.
// The v1 schema is intentionally small and constrained to HPA-managed Deployments.
type PredictiveScalingPolicySpec struct {
	TargetRef TargetReference `json:"targetRef"`

	// +kubebuilder:default:=recommendationOnly
	// +kubebuilder:validation:Enum=recommendationOnly
	Mode PredictiveScalingMode `json:"mode,omitempty"`

	// ForecastHorizon is the short-horizon lookahead for replay and recommendation evaluation.
	// +kubebuilder:default:="5m"
	ForecastHorizon metav1.Duration `json:"forecastHorizon,omitempty"`

	Warmup WarmupSettings `json:"warmup"`

	// +kubebuilder:default:=0.7
	// +kubebuilder:validation:Minimum=0.1
	// +kubebuilder:validation:Maximum=1
	ConfidenceThreshold float64 `json:"confidenceThreshold,omitempty"`

	// +kubebuilder:validation:Minimum=1
	MinReplicas int32 `json:"minReplicas"`

	// +kubebuilder:validation:Minimum=1
	MaxReplicas int32 `json:"maxReplicas"`

	// +optional
	ScaleUp *ScaleStepPolicy `json:"scaleUp,omitempty"`

	// +optional
	ScaleDown *ScaleStepPolicy `json:"scaleDown,omitempty"`

	// CooldownWindow is the stabilization period applied between surfaced recommendation changes.
	// +kubebuilder:default:="5m"
	CooldownWindow metav1.Duration `json:"cooldownWindow,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=16
	BlackoutWindows []BlackoutWindow `json:"blackoutWindows,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=16
	KnownEvents []KnownEvent `json:"knownEvents,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=8
	DependencyHealthChecks []DependencyHealthCheck `json:"dependencyHealthChecks,omitempty"`

	// +kubebuilder:default:=requireForScaleUp
	// +kubebuilder:validation:Enum=disabled;requireForScaleUp
	NodeHeadroomSanity NodeHeadroomSanityMode `json:"nodeHeadroomSanity,omitempty"`
}

// ObservedWorkloadIdentity reports the resolved workload the controller evaluated.
type ObservedWorkloadIdentity struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
	HPAName    string `json:"hpaName,omitempty"`
}

// SignalHealth reports readiness for one source signal.
type SignalHealth struct {
	Name    string            `json:"name,omitempty"`
	State   SignalHealthState `json:"state,omitempty"`
	Message string            `json:"message,omitempty"`
}

// TelemetryReadinessSummary reports whether the controller has enough signal quality to evaluate safely.
type TelemetryReadinessSummary struct {
	State     TelemetryReadinessState `json:"state,omitempty"`
	CheckedAt *metav1.Time            `json:"checkedAt,omitempty"`
	Message   string                  `json:"message,omitempty"`
	Signals   []SignalHealth          `json:"signals,omitempty"`
}

// ForecastSummary is the latest explainable forecast digest surfaced to operators.
type ForecastSummary struct {
	EvaluatedAt *metav1.Time    `json:"evaluatedAt,omitempty"`
	Method      string          `json:"method,omitempty"`
	Horizon     metav1.Duration `json:"horizon,omitempty"`
	Confidence  float64         `json:"confidence,omitempty"`
	Message     string          `json:"message,omitempty"`
}

// RecommendationSummary is the latest surfaced recommendation decision.
type RecommendationSummary struct {
	EvaluatedAt         *metav1.Time        `json:"evaluatedAt,omitempty"`
	State               RecommendationState `json:"state,omitempty"`
	BaselineReplicas    int32               `json:"baselineReplicas,omitempty"`
	RecommendedReplicas int32               `json:"recommendedReplicas,omitempty"`
	BoundedReplicas     int32               `json:"boundedReplicas,omitempty"`
	Message             string              `json:"message,omitempty"`
}

// SuppressionReason describes why a recommendation was not surfaced.
type SuppressionReason struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ReplaySummaryDigest points operators to the latest replay output or digest.
type ReplaySummaryDigest struct {
	WindowStart *metav1.Time `json:"windowStart,omitempty"`
	WindowEnd   *metav1.Time `json:"windowEnd,omitempty"`
	ReportRef   string       `json:"reportRef,omitempty"`
	Digest      string       `json:"digest,omitempty"`
}

// PredictiveScalingPolicyStatus holds the latest operator-facing evaluation state.
type PredictiveScalingPolicyStatus struct {
	ObservedWorkload   *ObservedWorkloadIdentity  `json:"observedWorkload,omitempty"`
	TelemetryReadiness *TelemetryReadinessSummary `json:"telemetryReadiness,omitempty"`
	LastForecast       *ForecastSummary           `json:"lastForecast,omitempty"`
	LastRecommendation *RecommendationSummary     `json:"lastRecommendation,omitempty"`
	SuppressionReasons []SuppressionReason        `json:"suppressionReasons,omitempty"`
	LastReplay         *ReplaySummaryDigest       `json:"lastReplay,omitempty"`
	Conditions         []metav1.Condition         `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=psp
// PredictiveScalingPolicy is the primary CRD for replay-backed burst-readiness evaluation.
type PredictiveScalingPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PredictiveScalingPolicySpec   `json:"spec,omitempty"`
	Status PredictiveScalingPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// PredictiveScalingPolicyList contains a list of PredictiveScalingPolicy objects.
type PredictiveScalingPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PredictiveScalingPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PredictiveScalingPolicy{}, &PredictiveScalingPolicyList{})
}
