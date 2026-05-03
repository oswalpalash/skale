package dashboard

import (
	"fmt"
	"sort"
	"strings"
	"time"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/discovery"
)

const (
	QualificationPolicyBacked = "policy-backed"

	ScalingContractHPA            = "hpa"
	ScalingContractExplicitPolicy = "explicitPolicy"
	ScalingContractMissing        = "missing"
	ScalingContractUnsupported    = "unsupported"
)

// Overview is the stable read-only contract used by the workload qualification dashboard.
type Overview struct {
	GeneratedAt time.Time  `json:"generatedAt"`
	InventoryAt time.Time  `json:"inventoryAt,omitempty"`
	Source      string     `json:"source,omitempty"`
	Summary     Summary    `json:"summary"`
	Workloads   []Workload `json:"workloads"`
}

// Summary aggregates workload qualification and live recommendation states.
type Summary struct {
	Total                     int `json:"total"`
	Candidates                int `json:"candidates"`
	PolicyBacked              int `json:"policyBacked"`
	NeedsConfiguration        int `json:"needsConfiguration"`
	NeedsScalingContract      int `json:"needsScalingContract"`
	LowConfidence             int `json:"lowConfidence"`
	Unsupported               int `json:"unsupported"`
	RecommendationAvailable   int `json:"recommendationAvailable"`
	RecommendationSuppressed  int `json:"recommendationSuppressed"`
	RecommendationUnavailable int `json:"recommendationUnavailable"`
}

// Workload is one row in the dashboard overview.
type Workload struct {
	ID                   string             `json:"id"`
	APIVersion           string             `json:"apiVersion,omitempty"`
	Kind                 string             `json:"kind,omitempty"`
	Namespace            string             `json:"namespace"`
	Name                 string             `json:"name"`
	Qualification        string             `json:"qualification"`
	ScalingContract      string             `json:"scalingContract"`
	HPAName              string             `json:"hpaName,omitempty"`
	Policy               *PolicyDigest      `json:"policy,omitempty"`
	TelemetryState       string             `json:"telemetryState,omitempty"`
	TelemetryMessage     string             `json:"telemetryMessage,omitempty"`
	ForecastMethod       string             `json:"forecastMethod,omitempty"`
	ForecastConfidence   *float64           `json:"forecastConfidence,omitempty"`
	RecommendationState  string             `json:"recommendationState,omitempty"`
	CurrentReplicas      *int32             `json:"currentReplicas,omitempty"`
	RecommendedReplicas  *int32             `json:"recommendedReplicas,omitempty"`
	SuppressionReasons   []string           `json:"suppressionReasons,omitempty"`
	MissingPrerequisites []string           `json:"missingPrerequisites,omitempty"`
	Reasons              []discovery.Reason `json:"reasons,omitempty"`
	NextAction           string             `json:"nextAction"`
}

// PolicyDigest summarizes the policy-backed live evaluation surface.
type PolicyDigest struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Mode       string `json:"mode,omitempty"`
	Generation int64  `json:"generation,omitempty"`
}

// BuildOverview merges cluster-wide discovery with policy status into a dashboard-facing view.
func BuildOverview(inventory discovery.Inventory, policies []skalev1alpha1.PredictiveScalingPolicy, generatedAt time.Time) Overview {
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	policiesByTarget := indexPolicies(policies)
	seen := map[string]struct{}{}

	workloads := make([]Workload, 0, len(inventory.Findings)+len(policies))
	for _, finding := range inventory.Findings {
		key := workloadKey(finding.Workload.Namespace, finding.Workload.Name)
		var policy *skalev1alpha1.PredictiveScalingPolicy
		if finding.Workload.Kind == "Deployment" {
			policy = policiesByTarget[key]
			seen[key] = struct{}{}
		}
		workload := workloadFromFinding(finding, policy)
		workloads = append(workloads, workload)
	}

	for key, policy := range policiesByTarget {
		if _, ok := seen[key]; ok {
			continue
		}
		workloads = append(workloads, workloadFromPolicy(policy))
	}

	sort.Slice(workloads, func(i, j int) bool {
		left, right := workloads[i], workloads[j]
		if qualificationRank(left.Qualification) != qualificationRank(right.Qualification) {
			return qualificationRank(left.Qualification) < qualificationRank(right.Qualification)
		}
		if left.Namespace != right.Namespace {
			return left.Namespace < right.Namespace
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Name < right.Name
	})

	overview := Overview{
		GeneratedAt: generatedAt.UTC(),
		InventoryAt: inventory.GeneratedAt.UTC(),
		Source:      "discovery inventory and PredictiveScalingPolicy status",
		Workloads:   workloads,
	}
	overview.Summary = summarize(workloads)
	return overview
}

func workloadFromFinding(finding discovery.Finding, policy *skalev1alpha1.PredictiveScalingPolicy) Workload {
	workload := Workload{
		ID:                   workloadKey(finding.Workload.Namespace, finding.Workload.Name),
		APIVersion:           finding.Workload.APIVersion,
		Kind:                 finding.Workload.Kind,
		Namespace:            finding.Workload.Namespace,
		Name:                 finding.Workload.Name,
		Qualification:        string(finding.Status),
		ScalingContract:      scalingContractFromFinding(finding),
		MissingPrerequisites: append([]string(nil), finding.MissingPrerequisites...),
		Reasons:              append([]discovery.Reason(nil), finding.Reasons...),
		NextAction:           nextActionFor(string(finding.Status), "", false),
	}
	if finding.HPA != nil {
		workload.HPAName = finding.HPA.Name
		if finding.HPA.CurrentReplicas > 0 {
			workload.CurrentReplicas = int32Ptr(finding.HPA.CurrentReplicas)
		}
	}
	if finding.TelemetryReadiness != nil {
		workload.TelemetryState = finding.TelemetryReadiness.State
		workload.TelemetryMessage = finding.TelemetryReadiness.Message
	}
	if finding.Predictability != nil {
		workload.ForecastMethod = finding.Predictability.ForecastMethod
		if finding.Predictability.ForecastConfidence > 0 {
			workload.ForecastConfidence = float64Ptr(finding.Predictability.ForecastConfidence)
		}
	}
	applyPolicy(&workload, policy)
	return workload
}

func workloadFromPolicy(policy *skalev1alpha1.PredictiveScalingPolicy) Workload {
	evaluated := policy.DeepCopy()
	evaluated.Default()
	name := evaluated.Spec.TargetRef.Name
	workload := Workload{
		ID:              workloadKey(evaluated.Namespace, name),
		APIVersion:      evaluated.Spec.TargetRef.APIVersion,
		Kind:            evaluated.Spec.TargetRef.Kind,
		Namespace:       evaluated.Namespace,
		Name:            name,
		Qualification:   QualificationPolicyBacked,
		ScalingContract: ScalingContractExplicitPolicy,
		NextAction:      nextActionFor(QualificationPolicyBacked, string(lastRecommendationState(evaluated.Status)), true),
	}
	applyPolicy(&workload, evaluated)
	return workload
}

func applyPolicy(workload *Workload, policy *skalev1alpha1.PredictiveScalingPolicy) {
	if policy == nil {
		return
	}
	evaluated := policy.DeepCopy()
	evaluated.Default()
	workload.Qualification = QualificationPolicyBacked
	if workload.ScalingContract == ScalingContractMissing || workload.ScalingContract == ScalingContractUnsupported || workload.ScalingContract == "" {
		workload.ScalingContract = ScalingContractExplicitPolicy
	}
	workload.Policy = &PolicyDigest{
		Namespace:  evaluated.Namespace,
		Name:       evaluated.Name,
		Mode:       string(evaluated.Spec.Mode),
		Generation: evaluated.Generation,
	}
	if observed := evaluated.Status.ObservedWorkload; observed != nil {
		if observed.Kind != "" {
			workload.Kind = observed.Kind
		}
		if observed.APIVersion != "" {
			workload.APIVersion = observed.APIVersion
		}
		if observed.HPAName != "" {
			workload.HPAName = observed.HPAName
		}
	}
	if telemetry := evaluated.Status.TelemetryReadiness; telemetry != nil {
		workload.TelemetryState = string(telemetry.State)
		workload.TelemetryMessage = telemetry.Message
	}
	if forecast := evaluated.Status.LastForecast; forecast != nil {
		workload.ForecastMethod = forecast.Method
		workload.ForecastConfidence = float64Ptr(forecast.Confidence)
	}
	if recommendation := evaluated.Status.LastRecommendation; recommendation != nil {
		workload.RecommendationState = string(recommendation.State)
		if recommendation.BaselineReplicas > 0 {
			workload.CurrentReplicas = int32Ptr(recommendation.BaselineReplicas)
		}
		if recommendation.State == skalev1alpha1.RecommendationStateAvailable ||
			recommendation.State == skalev1alpha1.RecommendationStateSuppressed {
			if recommendation.RecommendedReplicas > 0 {
				workload.RecommendedReplicas = int32Ptr(recommendation.RecommendedReplicas)
			}
		}
	}
	workload.SuppressionReasons = suppressionCodes(evaluated.Status.SuppressionReasons)
	workload.NextAction = nextActionFor(workload.Qualification, workload.RecommendationState, true)
}

func indexPolicies(policies []skalev1alpha1.PredictiveScalingPolicy) map[string]*skalev1alpha1.PredictiveScalingPolicy {
	byTarget := make(map[string]*skalev1alpha1.PredictiveScalingPolicy, len(policies))
	for index := range policies {
		policy := policies[index].DeepCopy()
		policy.Default()
		if policy.Spec.TargetRef.Name == "" {
			continue
		}
		key := workloadKey(policy.Namespace, policy.Spec.TargetRef.Name)
		if _, exists := byTarget[key]; exists {
			continue
		}
		byTarget[key] = policy
	}
	return byTarget
}

func scalingContractFromFinding(finding discovery.Finding) string {
	if finding.HPA != nil && finding.Workload.Kind == "Deployment" {
		return ScalingContractHPA
	}
	if finding.Status == discovery.StatusNeedsScalingContract {
		return ScalingContractMissing
	}
	return ScalingContractUnsupported
}

func nextActionFor(qualification, recommendationState string, policyBacked bool) string {
	if policyBacked {
		switch recommendationState {
		case string(skalev1alpha1.RecommendationStateAvailable):
			return "inspect recommendation evidence"
		case string(skalev1alpha1.RecommendationStateSuppressed):
			return "inspect suppression reasons before trial"
		case string(skalev1alpha1.RecommendationStateUnavailable):
			return "resolve unavailable recommendation inputs"
		default:
			return "wait for policy status or inspect controller configuration"
		}
	}
	switch qualification {
	case string(discovery.StatusCandidate):
		return "create a PredictiveScalingPolicy after telemetry prerequisites are clear"
	case string(discovery.StatusNeedsConfiguration):
		return "complete telemetry and policy prerequisites"
	case string(discovery.StatusNeedsScalingContract):
		return "add an HPA or explicit Skale scaling contract before recommendations"
	case string(discovery.StatusLowConfidence):
		return "review telemetry quality before trial"
	default:
		return "no replica recommendation is available for this workload"
	}
}

func summarize(workloads []Workload) Summary {
	summary := Summary{Total: len(workloads)}
	for _, workload := range workloads {
		switch workload.Qualification {
		case QualificationPolicyBacked:
			summary.PolicyBacked++
		case string(discovery.StatusCandidate):
			summary.Candidates++
		case string(discovery.StatusNeedsConfiguration):
			summary.NeedsConfiguration++
		case string(discovery.StatusNeedsScalingContract):
			summary.NeedsScalingContract++
		case string(discovery.StatusLowConfidence):
			summary.LowConfidence++
		default:
			summary.Unsupported++
		}
		switch workload.RecommendationState {
		case string(skalev1alpha1.RecommendationStateAvailable):
			summary.RecommendationAvailable++
		case string(skalev1alpha1.RecommendationStateSuppressed):
			summary.RecommendationSuppressed++
		case string(skalev1alpha1.RecommendationStateUnavailable):
			summary.RecommendationUnavailable++
		}
	}
	return summary
}

func qualificationRank(value string) int {
	switch value {
	case QualificationPolicyBacked:
		return 0
	case string(discovery.StatusCandidate):
		return 1
	case string(discovery.StatusNeedsConfiguration):
		return 2
	case string(discovery.StatusNeedsScalingContract):
		return 3
	case string(discovery.StatusLowConfidence):
		return 4
	default:
		return 5
	}
}

func suppressionCodes(reasons []skalev1alpha1.SuppressionReason) []string {
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		code := strings.TrimSpace(reason.Code)
		if code == "" {
			continue
		}
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

func workloadKey(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return fmt.Sprintf("%s/%s", namespace, name)
}

func int32Ptr(value int32) *int32 {
	return &value
}

func float64Ptr(value float64) *float64 {
	return &value
}

func lastRecommendationState(status skalev1alpha1.PredictiveScalingPolicyStatus) skalev1alpha1.RecommendationState {
	if status.LastRecommendation == nil {
		return ""
	}
	return status.LastRecommendation.State
}
