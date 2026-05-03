package discovery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/metrics"
)

const (
	defaultLookback              = 2 * time.Hour
	defaultForecastHorizon       = 5 * time.Minute
	defaultExpectedResolution    = 30 * time.Second
	defaultConfidenceThreshold   = 0.70
	defaultBurstRatioThreshold   = 1.50
	defaultSeasonalityConfidence = 0.55
)

// Status is the operator-facing discovery classification for one workload.
type Status string

const (
	StatusCandidate            Status = "candidate"
	StatusUnsupported          Status = "unsupported"
	StatusNeedsConfiguration   Status = "needs configuration"
	StatusNeedsScalingContract Status = "needs scaling contract"
	StatusLowConfidence        Status = "low confidence"
)

const (
	ReasonNoHPA                    = "no_hpa"
	ReasonScalingContractMissing   = "scaling_contract_missing"
	ReasonOutsideV1Wedge           = "outside_v1_wedge"
	ReasonTelemetryProviderMissing = "telemetry_provider_missing"
	ReasonTelemetryLoadFailed      = "telemetry_load_failed"
	ReasonTelemetryUnsupported     = "telemetry_unsupported"
	ReasonWarmupMissing            = "warmup_missing"
	ReasonTargetUtilizationMissing = "target_utilization_missing"
	ReasonForecastUnavailable      = "forecast_unavailable"
	ReasonForecastLowConfidence    = "forecast_low_confidence"
	ReasonBurstEvidenceWeak        = "burst_evidence_weak"
	ReasonPolicyAlreadyExists      = "policy_already_exists"
	ReasonReplayWorthRunning       = "replay_worth_running"
	ReasonHPAManagedDeployment     = "hpa_managed_deployment"
)

// Inventory is the cluster-wide discovery artifact written by the controller and consumed by operators.
type Inventory struct {
	GeneratedAt time.Time `json:"generatedAt"`
	Window      Window    `json:"window"`
	Scope       Scope     `json:"scope"`
	Summary     Summary   `json:"summary"`
	Findings    []Finding `json:"findings"`
}

// Window records the telemetry interval used for discovery hints.
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Scope describes the intentionally narrow v1 discovery boundary.
type Scope struct {
	WorkloadKinds []string `json:"workloadKinds"`
	Namespaces    string   `json:"namespaces"`
	Message       string   `json:"message"`
}

// Summary counts workload classifications in the inventory.
type Summary struct {
	Total                int `json:"total"`
	Candidates           int `json:"candidates"`
	Unsupported          int `json:"unsupported"`
	NeedsConfiguration   int `json:"needsConfiguration"`
	NeedsScalingContract int `json:"needsScalingContract"`
	LowConfidence        int `json:"lowConfidence"`
	PolicyBacked         int `json:"policyBacked"`
}

// Finding is one workload's discovery result.
type Finding struct {
	Status               Status              `json:"status"`
	Workload             WorkloadRef         `json:"workload"`
	HPA                  *HPASummary         `json:"hpa,omitempty"`
	ExistingPolicy       *PolicyRef          `json:"existingPolicy,omitempty"`
	TelemetryReadiness   *TelemetrySummary   `json:"telemetryReadiness,omitempty"`
	Burstiness           *BurstinessHint     `json:"burstiness,omitempty"`
	Predictability       *PredictabilityHint `json:"predictability,omitempty"`
	MissingPrerequisites []string            `json:"missingPrerequisites,omitempty"`
	Reasons              []Reason            `json:"reasons,omitempty"`
	PolicyDraft          string              `json:"policyDraft,omitempty"`
}

// WorkloadRef identifies the workload or unsupported HPA target under discovery.
type WorkloadRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

// HPASummary captures only the HPA data needed to explain fit and draft a policy.
type HPASummary struct {
	Name                    string   `json:"name"`
	MinReplicas             int32    `json:"minReplicas"`
	MaxReplicas             int32    `json:"maxReplicas"`
	CurrentReplicas         int32    `json:"currentReplicas,omitempty"`
	TargetUtilization       *float64 `json:"targetUtilization,omitempty"`
	TargetUtilizationSource string   `json:"targetUtilizationSource,omitempty"`
}

// PolicyRef marks that full evaluation is already explicit and policy-backed for the workload.
type PolicyRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// TelemetrySummary is a compact copy of readiness output for inventory JSON.
type TelemetrySummary struct {
	State   string          `json:"state"`
	Message string          `json:"message"`
	Signals []SignalSummary `json:"signals,omitempty"`
}

// SignalSummary reports one normalized telemetry signal.
type SignalSummary struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// BurstinessHint explains whether the demand shape looks worth replaying.
type BurstinessHint struct {
	Ratio   float64 `json:"ratio"`
	Message string  `json:"message"`
}

// PredictabilityHint explains the forecast/replay confidence signal used for discovery ranking.
type PredictabilityHint struct {
	ForecastMethod        string  `json:"forecastMethod,omitempty"`
	ForecastConfidence    float64 `json:"forecastConfidence,omitempty"`
	ForecastReliability   string  `json:"forecastReliability,omitempty"`
	SeasonalityDetected   bool    `json:"seasonalityDetected"`
	SeasonalitySeconds    int64   `json:"seasonalitySeconds,omitempty"`
	SeasonalityConfidence float64 `json:"seasonalityConfidence,omitempty"`
	Message               string  `json:"message"`
}

// Reason is an auditable explanation for a discovery classification.
type Reason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Scanner inventories common workload controllers and evaluates recommendation eligibility for the narrow scaling-contract wedge.
type Scanner struct {
	Reader                       client.Reader
	MetricsProvider              metrics.Provider
	ReadinessEvaluator           metrics.Evaluator
	ForecastModel                forecast.Model
	Now                          func() time.Time
	Lookback                     time.Duration
	ForecastHorizon              time.Duration
	ExpectedResolution           time.Duration
	ConfidenceThreshold          float64
	BurstRatioThreshold          float64
	IncludeDeploymentsWithoutHPA bool
}

// Scan builds a cluster-wide candidate inventory without generating live replica recommendations.
func (s Scanner) Scan(ctx context.Context) (Inventory, error) {
	if s.Reader == nil {
		return Inventory{}, errors.New("discovery scanner requires a Kubernetes reader")
	}

	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	lookback := s.Lookback
	if lookback <= 0 {
		lookback = defaultLookback
	}
	window := metrics.Window{Start: now.Add(-lookback), End: now}

	var deployments appsv1.DeploymentList
	if err := s.Reader.List(ctx, &deployments); err != nil {
		return Inventory{}, fmt.Errorf("list deployments for discovery: %w", err)
	}
	var hpas autoscalingv2.HorizontalPodAutoscalerList
	if err := s.Reader.List(ctx, &hpas); err != nil {
		return Inventory{}, fmt.Errorf("list horizontal pod autoscalers for discovery: %w", err)
	}
	var policies skalev1alpha1.PredictiveScalingPolicyList
	if err := s.Reader.List(ctx, &policies); err != nil {
		return Inventory{}, fmt.Errorf("list predictive scaling policies for discovery: %w", err)
	}
	var statefulSets appsv1.StatefulSetList
	if err := s.Reader.List(ctx, &statefulSets); err != nil {
		return Inventory{}, fmt.Errorf("list statefulsets for discovery: %w", err)
	}
	var daemonSets appsv1.DaemonSetList
	if err := s.Reader.List(ctx, &daemonSets); err != nil {
		return Inventory{}, fmt.Errorf("list daemonsets for discovery: %w", err)
	}
	var jobs batchv1.JobList
	if err := s.Reader.List(ctx, &jobs); err != nil {
		return Inventory{}, fmt.Errorf("list jobs for discovery: %w", err)
	}
	var cronJobs batchv1.CronJobList
	if err := s.Reader.List(ctx, &cronJobs); err != nil {
		return Inventory{}, fmt.Errorf("list cronjobs for discovery: %w", err)
	}

	hpasByDeployment, unsupportedHPAs := indexHPAs(hpas.Items)
	policiesByDeployment := indexPolicies(policies.Items)

	findings := make([]Finding, 0, len(deployments.Items)+len(unsupportedHPAs)+len(statefulSets.Items)+len(daemonSets.Items)+len(jobs.Items)+len(cronJobs.Items))
	seen := map[string]struct{}{}
	for _, hpa := range unsupportedHPAs {
		finding := unsupportedHPATargetFinding(hpa)
		findings = append(findings, finding)
		seen[workloadSeenKey(finding.Workload)] = struct{}{}
	}

	for _, deployment := range deployments.Items {
		key := namespacedName(deployment.Namespace, deployment.Name)
		matchingHPAs := hpasByDeployment[key]
		existingPolicy := policiesByDeployment[key]
		if len(matchingHPAs) == 0 {
			if s.IncludeDeploymentsWithoutHPA {
				finding := deploymentWithoutHPAFinding(deployment, existingPolicy)
				findings = append(findings, finding)
				seen[workloadSeenKey(finding.Workload)] = struct{}{}
			}
			continue
		}

		sort.Slice(matchingHPAs, func(i, j int) bool {
			return matchingHPAs[i].Name < matchingHPAs[j].Name
		})
		finding := s.evaluateHPADeployment(ctx, deployment, matchingHPAs[0], existingPolicy, window, now)
		findings = append(findings, finding)
		seen[workloadSeenKey(finding.Workload)] = struct{}{}
	}

	for _, statefulSet := range statefulSets.Items {
		ref := WorkloadRef{APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: statefulSet.Namespace, Name: statefulSet.Name}
		if _, ok := seen[workloadSeenKey(ref)]; ok {
			continue
		}
		findings = append(findings, unsupportedWorkloadFinding(ref, "StatefulSets are visible in the dashboard, but Skale does not produce predictive replica recommendations for StatefulSets in this release."))
		seen[workloadSeenKey(ref)] = struct{}{}
	}
	for _, daemonSet := range daemonSets.Items {
		ref := WorkloadRef{APIVersion: "apps/v1", Kind: "DaemonSet", Namespace: daemonSet.Namespace, Name: daemonSet.Name}
		if _, ok := seen[workloadSeenKey(ref)]; ok {
			continue
		}
		findings = append(findings, unsupportedWorkloadFinding(ref, "DaemonSet replica count is node-driven, so Skale does not produce pod replica recommendations for this workload."))
		seen[workloadSeenKey(ref)] = struct{}{}
	}
	for _, job := range jobs.Items {
		ref := WorkloadRef{APIVersion: "batch/v1", Kind: "Job", Namespace: job.Namespace, Name: job.Name}
		if _, ok := seen[workloadSeenKey(ref)]; ok {
			continue
		}
		findings = append(findings, unsupportedWorkloadFinding(ref, "Jobs are finite-run workloads; Skale does not produce predictive Deployment-style replica recommendations for them."))
		seen[workloadSeenKey(ref)] = struct{}{}
	}
	for _, cronJob := range cronJobs.Items {
		ref := WorkloadRef{APIVersion: "batch/v1", Kind: "CronJob", Namespace: cronJob.Namespace, Name: cronJob.Name}
		if _, ok := seen[workloadSeenKey(ref)]; ok {
			continue
		}
		findings = append(findings, unsupportedWorkloadFinding(ref, "CronJobs already have scheduled execution semantics; Skale does not produce predictive Deployment-style replica recommendations for them."))
		seen[workloadSeenKey(ref)] = struct{}{}
	}

	sort.Slice(findings, func(i, j int) bool {
		left, right := findings[i], findings[j]
		if rankStatus(left.Status) != rankStatus(right.Status) {
			return rankStatus(left.Status) < rankStatus(right.Status)
		}
		if left.Workload.Namespace != right.Workload.Namespace {
			return left.Workload.Namespace < right.Workload.Namespace
		}
		return left.Workload.Name < right.Workload.Name
	})

	inventory := Inventory{
		GeneratedAt: now,
		Window: Window{
			Start: window.Start,
			End:   window.End,
		},
		Scope: Scope{
			WorkloadKinds: []string{"apps/v1.Deployment", "apps/v1.StatefulSet", "apps/v1.DaemonSet", "batch/v1.Job", "batch/v1.CronJob", "autoscaling/v2.HorizontalPodAutoscaler"},
			Namespaces:    "all",
			Message:       "Discovery inventory is broad enough to show common Kubernetes workload controllers, while replica recommendation eligibility remains limited to workloads with an explicit scaling contract.",
		},
		Findings: findings,
	}
	inventory.Summary = summarize(findings)
	return inventory, nil
}

func (s Scanner) evaluateHPADeployment(
	ctx context.Context,
	deployment appsv1.Deployment,
	hpa autoscalingv2.HorizontalPodAutoscaler,
	existingPolicy *PolicyRef,
	window metrics.Window,
	now time.Time,
) Finding {
	hpaSummary := summarizeHPA(hpa)
	finding := Finding{
		Status:         StatusNeedsConfiguration,
		Workload:       deploymentRef(deployment),
		HPA:            &hpaSummary,
		ExistingPolicy: existingPolicy,
		Reasons: []Reason{{
			Code:    ReasonHPAManagedDeployment,
			Message: fmt.Sprintf("Deployment %s/%s is targeted by HPA %q.", deployment.Namespace, deployment.Name, hpa.Name),
		}},
	}
	if existingPolicy != nil {
		finding.Reasons = append(finding.Reasons, Reason{
			Code:    ReasonPolicyAlreadyExists,
			Message: fmt.Sprintf("Full recommendation evaluation is already policy-backed by %s/%s.", existingPolicy.Namespace, existingPolicy.Name),
		})
	}
	if hpaSummary.TargetUtilization == nil {
		finding.MissingPrerequisites = append(finding.MissingPrerequisites, "target utilization")
		finding.Reasons = append(finding.Reasons, Reason{
			Code:    ReasonTargetUtilizationMissing,
			Message: "HPA target utilization could not be inferred; generated policy must be reviewed before use.",
		})
	}
	finding.PolicyDraft = BuildPolicyDraft(finding)

	if s.MetricsProvider == nil {
		finding.MissingPrerequisites = append(finding.MissingPrerequisites, "Prometheus query mapping")
		finding.Reasons = append(finding.Reasons, Reason{
			Code:    ReasonTelemetryProviderMissing,
			Message: "No metrics provider is configured, so discovery cannot validate demand, replica, CPU, memory, or warmup telemetry.",
		})
		return finding
	}

	snapshot, err := s.MetricsProvider.LoadWindow(ctx, metrics.Target{Namespace: deployment.Namespace, Name: deployment.Name}, window)
	if err != nil {
		finding.MissingPrerequisites = append(finding.MissingPrerequisites, "Prometheus query mapping")
		finding.Reasons = append(finding.Reasons, Reason{
			Code:    ReasonTelemetryLoadFailed,
			Message: fmt.Sprintf("Telemetry could not be loaded for discovery: %v.", err),
		})
		return finding
	}

	readinessEvaluator := s.ReadinessEvaluator
	if readinessEvaluator == nil {
		readinessEvaluator = metrics.DefaultEvaluator{}
	}
	readiness, err := readinessEvaluator.Evaluate(metrics.ReadinessInput{
		EvaluatedAt: now,
		Snapshot:    snapshot,
		Options:     s.readinessOptions(),
	})
	if err != nil {
		finding.Reasons = append(finding.Reasons, Reason{
			Code:    ReasonTelemetryUnsupported,
			Message: fmt.Sprintf("Telemetry readiness evaluation failed: %v.", err),
		})
		finding.Status = StatusUnsupported
		return finding
	}
	finding.TelemetryReadiness = readinessSummary(readiness)

	if readiness.Level == metrics.ReadinessLevelUnsupported {
		if warmupOnlyBlocking(readiness) {
			finding.Status = StatusNeedsConfiguration
			finding.MissingPrerequisites = append(finding.MissingPrerequisites, "warmup")
			finding.Reasons = append(finding.Reasons, Reason{
				Code:    ReasonWarmupMissing,
				Message: "Demand, replica, CPU, and memory telemetry are present, but warmup lag is neither configured nor estimable.",
			})
			return finding
		}
		finding.Status = StatusUnsupported
		finding.Reasons = append(finding.Reasons, Reason{
			Code:    ReasonTelemetryUnsupported,
			Message: readiness.Summary,
		})
		return finding
	}

	burstiness := demandBurstiness(snapshot.Demand)
	finding.Burstiness = &burstiness
	predictability, forecastOK := s.predictability(ctx, snapshot, now)
	finding.Predictability = &predictability

	if hpaSummary.TargetUtilization == nil {
		return finding
	}
	if !forecastOK {
		finding.Status = StatusLowConfidence
		finding.Reasons = append(finding.Reasons, Reason{
			Code:    ReasonForecastLowConfidence,
			Message: predictability.Message,
		})
		return finding
	}
	if burstiness.Ratio < s.burstRatioThreshold() && !predictability.SeasonalityDetected {
		finding.Status = StatusLowConfidence
		finding.Reasons = append(finding.Reasons, Reason{
			Code:    ReasonBurstEvidenceWeak,
			Message: "Telemetry exists, but demand does not show enough burstiness or recurrence to prioritize replay analysis.",
		})
		return finding
	}

	finding.Status = StatusCandidate
	finding.Reasons = append(finding.Reasons, Reason{
		Code:    ReasonReplayWorthRunning,
		Message: "HPA-managed Deployment has usable telemetry and enough burst or predictability evidence to run replay before applying a policy.",
	})
	return finding
}

func (s Scanner) predictability(ctx context.Context, snapshot metrics.Snapshot, now time.Time) (PredictabilityHint, bool) {
	points := forecastPoints(snapshot.Demand)
	seasonality := forecast.DetectSeasonality(points, forecast.SeasonalityDetectionOptions{
		MinPeriod:      minSeasonalityPeriod(points),
		MaxPeriod:      maxSeasonalityPeriod(points),
		MinCycles:      3,
		MinCorrelation: 0.75,
	})
	hint := PredictabilityHint{
		SeasonalityDetected:   seasonality.Detected,
		SeasonalityConfidence: seasonality.Confidence,
		Message:               seasonality.Message,
	}
	if seasonality.Period > 0 {
		hint.SeasonalitySeconds = int64(seasonality.Period / time.Second)
	}

	model := s.ForecastModel
	if model == nil {
		model = forecast.AutoModel{}
	}
	horizon := s.ForecastHorizon
	if horizon <= 0 {
		horizon = defaultForecastHorizon
	}
	result, err := model.Forecast(ctx, forecast.Input{
		Series:                points,
		EvaluatedAt:           now,
		Horizon:               horizon,
		Seasonality:           seasonality.Period,
		SeasonalitySource:     forecast.SeasonalitySourceDetected,
		SeasonalityConfidence: seasonality.Confidence,
	})
	if err != nil {
		hint.Message = fmt.Sprintf("Forecast quality could not be established during discovery: %v.", err)
		return hint, false
	}
	hint.ForecastMethod = result.Model
	hint.ForecastConfidence = result.Confidence
	hint.ForecastReliability = string(result.Reliability)
	if hint.Message == "" {
		hint.Message = "Forecast quality was evaluated from discovery telemetry."
	}

	ok := result.Reliability != forecast.ReliabilityUnsupported &&
		result.Reliability != forecast.ReliabilityLow &&
		result.Confidence >= s.confidenceThreshold()
	if seasonality.Detected && seasonality.Confidence >= defaultSeasonalityConfidence && result.Confidence >= s.confidenceThreshold()-0.10 {
		ok = ok || result.Reliability == forecast.ReliabilityMedium
	}
	if !ok {
		hint.Message = fmt.Sprintf(
			"Forecast confidence %.2f with %s reliability is too weak for candidate status.",
			result.Confidence,
			result.Reliability,
		)
	}
	return hint, ok
}

func (s Scanner) readinessOptions() metrics.ReadinessOptions {
	options := metrics.DefaultReadinessOptions()
	options.MinimumLookback = s.Lookback
	if options.MinimumLookback <= 0 {
		options.MinimumLookback = defaultLookback
	}
	options.ExpectedResolution = s.ExpectedResolution
	if options.ExpectedResolution <= 0 {
		options.ExpectedResolution = defaultExpectedResolution
	}
	return options
}

func (s Scanner) confidenceThreshold() float64 {
	if s.ConfidenceThreshold <= 0 {
		return defaultConfidenceThreshold
	}
	return s.ConfidenceThreshold
}

func (s Scanner) burstRatioThreshold() float64 {
	if s.BurstRatioThreshold <= 0 {
		return defaultBurstRatioThreshold
	}
	return s.BurstRatioThreshold
}

func indexHPAs(hpas []autoscalingv2.HorizontalPodAutoscaler) (map[string][]autoscalingv2.HorizontalPodAutoscaler, []autoscalingv2.HorizontalPodAutoscaler) {
	byDeployment := make(map[string][]autoscalingv2.HorizontalPodAutoscaler)
	var unsupported []autoscalingv2.HorizontalPodAutoscaler
	for _, hpa := range hpas {
		if hpa.Spec.ScaleTargetRef.Kind != "Deployment" {
			unsupported = append(unsupported, hpa)
			continue
		}
		byDeployment[namespacedName(hpa.Namespace, hpa.Spec.ScaleTargetRef.Name)] = append(
			byDeployment[namespacedName(hpa.Namespace, hpa.Spec.ScaleTargetRef.Name)],
			hpa,
		)
	}
	return byDeployment, unsupported
}

func indexPolicies(policies []skalev1alpha1.PredictiveScalingPolicy) map[string]*PolicyRef {
	byDeployment := make(map[string]*PolicyRef)
	for _, policy := range policies {
		evaluated := policy.DeepCopy()
		evaluated.Default()
		if evaluated.Spec.TargetRef.Kind != "" && evaluated.Spec.TargetRef.Kind != "Deployment" {
			continue
		}
		key := namespacedName(policy.Namespace, evaluated.Spec.TargetRef.Name)
		if _, exists := byDeployment[key]; exists {
			continue
		}
		byDeployment[key] = &PolicyRef{Namespace: policy.Namespace, Name: policy.Name}
	}
	return byDeployment
}

func unsupportedHPATargetFinding(hpa autoscalingv2.HorizontalPodAutoscaler) Finding {
	target := hpa.Spec.ScaleTargetRef
	return Finding{
		Status: StatusUnsupported,
		Workload: WorkloadRef{
			APIVersion: target.APIVersion,
			Kind:       target.Kind,
			Namespace:  hpa.Namespace,
			Name:       target.Name,
		},
		HPA: ptrHPASummary(summarizeHPA(hpa)),
		Reasons: []Reason{{
			Code:    ReasonOutsideV1Wedge,
			Message: fmt.Sprintf("HPA %s/%s targets %s %q; v1 discovery is limited to HPA-managed Deployments.", hpa.Namespace, hpa.Name, target.Kind, target.Name),
		}},
	}
}

func deploymentWithoutHPAFinding(deployment appsv1.Deployment, existingPolicy *PolicyRef) Finding {
	finding := Finding{
		Status:         StatusNeedsScalingContract,
		Workload:       deploymentRef(deployment),
		ExistingPolicy: existingPolicy,
		Reasons: []Reason{{
			Code:    ReasonScalingContractMissing,
			Message: "Deployment is not targeted by an HPA and has no explicit Skale scaling contract; replica recommendations are withheld until min/max replicas, demand signal, warmup, and safety policy are explicit.",
		}},
	}
	finding.MissingPrerequisites = append(finding.MissingPrerequisites, "scaling contract")
	if existingPolicy != nil {
		finding.Reasons = append(finding.Reasons, Reason{
			Code:    ReasonPolicyAlreadyExists,
			Message: fmt.Sprintf("A PredictiveScalingPolicy %s/%s exists, but discovery did not find a matching HPA.", existingPolicy.Namespace, existingPolicy.Name),
		})
	}
	return finding
}

func unsupportedWorkloadFinding(ref WorkloadRef, message string) Finding {
	return Finding{
		Status:   StatusUnsupported,
		Workload: ref,
		Reasons: []Reason{{
			Code:    ReasonOutsideV1Wedge,
			Message: message,
		}},
	}
}

func summarizeHPA(hpa autoscalingv2.HorizontalPodAutoscaler) HPASummary {
	minReplicas := int32(1)
	if hpa.Spec.MinReplicas != nil {
		minReplicas = *hpa.Spec.MinReplicas
	}
	utilization, source := targetUtilization(hpa)
	return HPASummary{
		Name:                    hpa.Name,
		MinReplicas:             minReplicas,
		MaxReplicas:             hpa.Spec.MaxReplicas,
		CurrentReplicas:         hpa.Status.CurrentReplicas,
		TargetUtilization:       utilization,
		TargetUtilizationSource: source,
	}
}

func targetUtilization(hpa autoscalingv2.HorizontalPodAutoscaler) (*float64, string) {
	for _, metric := range hpa.Spec.Metrics {
		if metric.Type != autoscalingv2.ResourceMetricSourceType || metric.Resource == nil {
			continue
		}
		if metric.Resource.Name != "cpu" || metric.Resource.Target.Type != autoscalingv2.UtilizationMetricType {
			continue
		}
		if metric.Resource.Target.AverageUtilization == nil {
			continue
		}
		value := float64(*metric.Resource.Target.AverageUtilization) / 100
		return &value, "hpa_cpu_average_utilization"
	}
	return nil, ""
}

func readinessSummary(report metrics.ReadinessReport) *TelemetrySummary {
	summary := &TelemetrySummary{
		State:   string(report.Level),
		Message: report.Summary,
		Signals: make([]SignalSummary, 0, len(report.Signals)),
	}
	for _, signal := range report.Signals {
		summary.Signals = append(summary.Signals, SignalSummary{
			Name:    string(signal.Name),
			State:   string(signal.Level),
			Message: signal.Message,
		})
	}
	return summary
}

func warmupOnlyBlocking(report metrics.ReadinessReport) bool {
	if len(report.BlockingReasons) == 0 {
		return false
	}
	for _, signal := range report.Signals {
		if signal.Name == metrics.SignalWarmup {
			continue
		}
		if signal.Required && (signal.Level == metrics.SignalLevelUnsupported || signal.Level == metrics.SignalLevelMissing) {
			return false
		}
	}
	return true
}

func demandBurstiness(series metrics.SignalSeries) BurstinessHint {
	values := make([]float64, 0, len(series.Samples))
	for _, sample := range series.Samples {
		if sample.Value > 0 && !math.IsNaN(sample.Value) && !math.IsInf(sample.Value, 0) {
			values = append(values, sample.Value)
		}
	}
	if len(values) == 0 {
		return BurstinessHint{Message: "Demand signal had no positive samples."}
	}
	sort.Float64s(values)
	median := values[len(values)/2]
	if len(values)%2 == 0 {
		median = (values[len(values)/2-1] + values[len(values)/2]) / 2
	}
	peak := values[len(values)-1]
	if median <= 0 {
		return BurstinessHint{Message: "Demand median is zero, so burst ratio is not reliable."}
	}
	ratio := peak / median
	return BurstinessHint{
		Ratio:   ratio,
		Message: fmt.Sprintf("Peak demand is %.2fx the median over the discovery window.", ratio),
	}
}

func forecastPoints(series metrics.SignalSeries) []forecast.Point {
	points := make([]forecast.Point, 0, len(series.Samples))
	for _, sample := range series.Samples {
		points = append(points, forecast.Point{Timestamp: sample.Timestamp, Value: sample.Value})
	}
	sort.Slice(points, func(i, j int) bool {
		return points[i].Timestamp.Before(points[j].Timestamp)
	})
	return points
}

func minSeasonalityPeriod(points []forecast.Point) time.Duration {
	step := inferStep(points)
	if step <= 0 {
		return 0
	}
	return 2 * step
}

func maxSeasonalityPeriod(points []forecast.Point) time.Duration {
	step := inferStep(points)
	if step <= 0 || len(points) < 6 {
		return 0
	}
	return time.Duration(len(points)/3) * step
}

func inferStep(points []forecast.Point) time.Duration {
	if len(points) < 2 {
		return 0
	}
	deltas := make([]int64, 0, len(points)-1)
	for index := 1; index < len(points); index++ {
		delta := points[index].Timestamp.Sub(points[index-1].Timestamp)
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

func deploymentRef(deployment appsv1.Deployment) WorkloadRef {
	return WorkloadRef{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Namespace:  deployment.Namespace,
		Name:       deployment.Name,
	}
}

func summarize(findings []Finding) Summary {
	summary := Summary{Total: len(findings)}
	for _, finding := range findings {
		switch finding.Status {
		case StatusCandidate:
			summary.Candidates++
		case StatusUnsupported:
			summary.Unsupported++
		case StatusNeedsConfiguration:
			summary.NeedsConfiguration++
		case StatusNeedsScalingContract:
			summary.NeedsScalingContract++
		case StatusLowConfidence:
			summary.LowConfidence++
		}
		if finding.ExistingPolicy != nil {
			summary.PolicyBacked++
		}
	}
	return summary
}

func rankStatus(status Status) int {
	switch status {
	case StatusCandidate:
		return 0
	case StatusNeedsConfiguration:
		return 1
	case StatusNeedsScalingContract:
		return 2
	case StatusLowConfidence:
		return 3
	default:
		return 4
	}
}

func namespacedName(namespace, name string) string {
	return namespace + "/" + name
}

func workloadSeenKey(ref WorkloadRef) string {
	return ref.APIVersion + "/" + ref.Kind + "/" + ref.Namespace + "/" + ref.Name
}

func ptrHPASummary(summary HPASummary) *HPASummary {
	return &summary
}

// BuildPolicyDraft renders a conservative PredictiveScalingPolicy manifest for review.
func BuildPolicyDraft(finding Finding) string {
	if finding.HPA == nil || finding.Workload.Kind != "Deployment" || finding.Workload.Name == "" {
		return ""
	}
	targetUtilization := defaultTargetUtilization(finding.HPA.TargetUtilization)
	name := policyName(finding.Workload.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: skale.io/v1alpha1\n")
	fmt.Fprintf(&b, "kind: PredictiveScalingPolicy\n")
	fmt.Fprintf(&b, "metadata:\n")
	fmt.Fprintf(&b, "  name: %s\n", name)
	fmt.Fprintf(&b, "  namespace: %s\n", finding.Workload.Namespace)
	fmt.Fprintf(&b, "spec:\n")
	fmt.Fprintf(&b, "  targetRef:\n")
	fmt.Fprintf(&b, "    apiVersion: apps/v1\n")
	fmt.Fprintf(&b, "    kind: Deployment\n")
	fmt.Fprintf(&b, "    name: %s\n", finding.Workload.Name)
	fmt.Fprintf(&b, "  mode: recommendationOnly\n")
	fmt.Fprintf(&b, "  forecastHorizon: 5m\n")
	fmt.Fprintf(&b, "  warmup:\n")
	fmt.Fprintf(&b, "    estimatedReadyDuration: 45s\n")
	fmt.Fprintf(&b, "  targetUtilization: %.2f\n", targetUtilization)
	fmt.Fprintf(&b, "  confidenceThreshold: %.2f\n", defaultConfidenceThreshold)
	fmt.Fprintf(&b, "  minReplicas: %d\n", finding.HPA.MinReplicas)
	fmt.Fprintf(&b, "  maxReplicas: %d\n", finding.HPA.MaxReplicas)
	fmt.Fprintf(&b, "  scaleUp:\n")
	fmt.Fprintf(&b, "    maxReplicasChange: %d\n", defaultMaxStepUp(finding.HPA))
	fmt.Fprintf(&b, "  scaleDown:\n")
	fmt.Fprintf(&b, "    maxReplicasChange: 1\n")
	fmt.Fprintf(&b, "  cooldownWindow: 5m\n")
	fmt.Fprintf(&b, "  nodeHeadroomSanity: requireForScaleUp\n")
	return b.String()
}

func defaultTargetUtilization(value *float64) float64 {
	if value != nil && *value > 0 && *value <= 1 {
		return *value
	}
	return 0.80
}

func defaultMaxStepUp(hpa *HPASummary) int32 {
	if hpa == nil || hpa.MaxReplicas <= hpa.MinReplicas {
		return 1
	}
	delta := hpa.MaxReplicas - hpa.MinReplicas
	if delta < 2 {
		return 1
	}
	if delta > 4 {
		return 4
	}
	return delta
}

func policyName(workloadName string) string {
	name := workloadName + "-burst-readiness"
	if len(name) <= 63 {
		return name
	}
	return strings.TrimRight(name[:63], "-")
}
