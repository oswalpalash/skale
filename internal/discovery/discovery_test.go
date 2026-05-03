package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/metrics"
)

func TestScannerClassifiesHPADeploymentCandidate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	reader := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testDeployment("payments", "checkout-api"), testHPA("payments", "checkout-hpa", "checkout-api")).
		Build()

	inventory, err := Scanner{
		Reader:                       reader,
		MetricsProvider:              staticProvider{snapshot: discoverySnapshot(now.Add(-2*time.Hour), true, []float64{120, 120, 120, 260, 300, 260})},
		ForecastModel:                staticForecast{result: forecastResult(now, 0.86, forecast.ReliabilityHigh)},
		Now:                          func() time.Time { return now },
		Lookback:                     2 * time.Hour,
		ExpectedResolution:           5 * time.Minute,
		IncludeDeploymentsWithoutHPA: true,
	}.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	finding := onlyFinding(t, inventory, "payments", "checkout-api")
	if finding.Status != StatusCandidate {
		t.Fatalf("status = %q, want %q; reasons=%#v", finding.Status, StatusCandidate, finding.Reasons)
	}
	if finding.HPA == nil || finding.HPA.TargetUtilization == nil || *finding.HPA.TargetUtilization != 0.7 {
		t.Fatalf("unexpected HPA summary %#v", finding.HPA)
	}
	if finding.TelemetryReadiness == nil || finding.TelemetryReadiness.State != string(metrics.ReadinessLevelDegraded) {
		t.Fatalf("unexpected telemetry readiness %#v", finding.TelemetryReadiness)
	}
	if finding.Burstiness == nil || finding.Burstiness.Ratio < 2 {
		t.Fatalf("unexpected burstiness %#v", finding.Burstiness)
	}
	if !containsReason(finding, ReasonReplayWorthRunning) {
		t.Fatalf("expected replay-worth-running reason, got %#v", finding.Reasons)
	}
	if !strings.Contains(finding.PolicyDraft, "kind: PredictiveScalingPolicy") ||
		!strings.Contains(finding.PolicyDraft, "targetUtilization: 0.70") ||
		!strings.Contains(finding.PolicyDraft, "nodeHeadroomSanity: requireForScaleUp") {
		t.Fatalf("unexpected policy draft:\n%s", finding.PolicyDraft)
	}
	if inventory.Summary.Candidates != 1 || inventory.Summary.Total != 1 {
		t.Fatalf("unexpected summary %#v", inventory.Summary)
	}
}

func TestScannerMarksWarmupGapNeedsConfiguration(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	reader := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testDeployment("payments", "checkout-api"), testHPA("payments", "checkout-hpa", "checkout-api")).
		Build()

	inventory, err := Scanner{
		Reader:                       reader,
		MetricsProvider:              staticProvider{snapshot: discoverySnapshot(now.Add(-2*time.Hour), false, []float64{120, 120, 120, 260, 300, 260})},
		ForecastModel:                staticForecast{result: forecastResult(now, 0.86, forecast.ReliabilityHigh)},
		Now:                          func() time.Time { return now },
		Lookback:                     2 * time.Hour,
		ExpectedResolution:           5 * time.Minute,
		IncludeDeploymentsWithoutHPA: true,
	}.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	finding := onlyFinding(t, inventory, "payments", "checkout-api")
	if finding.Status != StatusNeedsConfiguration {
		t.Fatalf("status = %q, want %q; reasons=%#v", finding.Status, StatusNeedsConfiguration, finding.Reasons)
	}
	if !containsReason(finding, ReasonWarmupMissing) {
		t.Fatalf("expected warmup missing reason, got %#v", finding.Reasons)
	}
	if !containsString(finding.MissingPrerequisites, "warmup") {
		t.Fatalf("expected warmup missing prerequisite, got %#v", finding.MissingPrerequisites)
	}
}

func TestScannerMarksNoHPADeploymentNeedsScalingContract(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	reader := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testDeployment("payments", "checkout-api")).
		Build()

	inventory, err := Scanner{
		Reader:                       reader,
		Now:                          func() time.Time { return now },
		IncludeDeploymentsWithoutHPA: true,
	}.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	finding := onlyFinding(t, inventory, "payments", "checkout-api")
	if finding.Status != StatusNeedsScalingContract {
		t.Fatalf("status = %q, want %q", finding.Status, StatusNeedsScalingContract)
	}
	if !containsReason(finding, ReasonScalingContractMissing) {
		t.Fatalf("expected scaling-contract-missing reason, got %#v", finding.Reasons)
	}
	if !containsString(finding.MissingPrerequisites, "scaling contract") {
		t.Fatalf("expected scaling contract missing prerequisite, got %#v", finding.MissingPrerequisites)
	}
	if inventory.Summary.NeedsScalingContract != 1 {
		t.Fatalf("needsScalingContract = %d, want 1", inventory.Summary.NeedsScalingContract)
	}
}

func TestScannerMarksNonDeploymentHPATargetUnsupported(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	reader := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testStatefulSetHPA("payments", "checkout-stateful-hpa", "checkout-stateful")).
		Build()

	inventory, err := Scanner{
		Reader:                       reader,
		Now:                          func() time.Time { return now },
		IncludeDeploymentsWithoutHPA: true,
	}.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	finding := onlyFinding(t, inventory, "payments", "checkout-stateful")
	if finding.Status != StatusUnsupported {
		t.Fatalf("status = %q, want %q", finding.Status, StatusUnsupported)
	}
	if finding.Workload.Kind != "StatefulSet" {
		t.Fatalf("workload kind = %q, want StatefulSet", finding.Workload.Kind)
	}
	if !containsReason(finding, ReasonOutsideV1Wedge) {
		t.Fatalf("expected outside-v1 reason, got %#v", finding.Reasons)
	}
}

func TestScannerInventoriesCommonUnsupportedWorkloadControllers(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	reader := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(
			testStatefulSet("payments", "orders-db"),
			testDaemonSet("observability", "node-agent"),
			testJob("payments", "backfill"),
			testCronJob("payments", "nightly-close"),
		).
		Build()

	inventory, err := Scanner{
		Reader: reader,
		Now:    func() time.Time { return now },
	}.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	expected := map[string]string{
		"payments/orders-db":       "StatefulSet",
		"observability/node-agent": "DaemonSet",
		"payments/backfill":        "Job",
		"payments/nightly-close":   "CronJob",
	}
	for id, kind := range expected {
		namespace, name, _ := strings.Cut(id, "/")
		finding := onlyFinding(t, inventory, namespace, name)
		if finding.Workload.Kind != kind {
			t.Fatalf("%s kind = %q, want %q", id, finding.Workload.Kind, kind)
		}
		if finding.Status != StatusUnsupported {
			t.Fatalf("%s status = %q, want unsupported", id, finding.Status)
		}
		if !containsReason(finding, ReasonOutsideV1Wedge) {
			t.Fatalf("%s expected outside-v1 reason, got %#v", id, finding.Reasons)
		}
	}
	if inventory.Summary.Unsupported != 4 || inventory.Summary.Total != 4 {
		t.Fatalf("unexpected summary %#v", inventory.Summary)
	}
}

func TestScannerMarksMissingDemandSignalUnsupported(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	snapshot := discoverySnapshot(now.Add(-2*time.Hour), true, []float64{120, 120, 120, 260, 300, 260})
	snapshot.Demand.Samples = nil
	reader := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testDeployment("payments", "checkout-api"), testHPA("payments", "checkout-hpa", "checkout-api")).
		Build()

	inventory, err := Scanner{
		Reader:                       reader,
		MetricsProvider:              staticProvider{snapshot: snapshot},
		ForecastModel:                staticForecast{result: forecastResult(now, 0.86, forecast.ReliabilityHigh)},
		Now:                          func() time.Time { return now },
		Lookback:                     2 * time.Hour,
		ExpectedResolution:           5 * time.Minute,
		IncludeDeploymentsWithoutHPA: true,
	}.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	finding := onlyFinding(t, inventory, "payments", "checkout-api")
	if finding.Status != StatusUnsupported {
		t.Fatalf("status = %q, want %q; reasons=%#v", finding.Status, StatusUnsupported, finding.Reasons)
	}
	if !containsReason(finding, ReasonTelemetryUnsupported) {
		t.Fatalf("expected telemetry unsupported reason, got %#v", finding.Reasons)
	}
}

func TestScannerMarksForecastWeakLowConfidence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	reader := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testDeployment("payments", "checkout-api"), testHPA("payments", "checkout-hpa", "checkout-api")).
		Build()

	inventory, err := Scanner{
		Reader:                       reader,
		MetricsProvider:              staticProvider{snapshot: discoverySnapshot(now.Add(-2*time.Hour), true, []float64{120, 120, 120, 260, 300, 260})},
		ForecastModel:                staticForecast{result: forecastResult(now, 0.40, forecast.ReliabilityLow)},
		Now:                          func() time.Time { return now },
		Lookback:                     2 * time.Hour,
		ExpectedResolution:           5 * time.Minute,
		IncludeDeploymentsWithoutHPA: true,
	}.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	finding := onlyFinding(t, inventory, "payments", "checkout-api")
	if finding.Status != StatusLowConfidence {
		t.Fatalf("status = %q, want %q; reasons=%#v", finding.Status, StatusLowConfidence, finding.Reasons)
	}
	if !containsReason(finding, ReasonForecastLowConfidence) {
		t.Fatalf("expected low-confidence reason, got %#v", finding.Reasons)
	}
}

func TestScannerMarksTelemetryLoadFailureNeedsConfiguration(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	reader := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testDeployment("payments", "checkout-api"), testHPA("payments", "checkout-hpa", "checkout-api")).
		Build()

	inventory, err := Scanner{
		Reader:                       reader,
		MetricsProvider:              failingProvider{err: errors.New("query returned no demand series")},
		Now:                          func() time.Time { return now },
		IncludeDeploymentsWithoutHPA: true,
	}.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	finding := onlyFinding(t, inventory, "payments", "checkout-api")
	if finding.Status != StatusNeedsConfiguration {
		t.Fatalf("status = %q, want %q", finding.Status, StatusNeedsConfiguration)
	}
	if !containsReason(finding, ReasonTelemetryLoadFailed) {
		t.Fatalf("expected telemetry load failure reason, got %#v", finding.Reasons)
	}
	if !containsString(finding.MissingPrerequisites, "Prometheus query mapping") {
		t.Fatalf("unexpected missing prerequisites %#v", finding.MissingPrerequisites)
	}
}

func TestScannerRecordsExistingPolicyWithoutTreatingDiscoveryAsRecommendation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	policy := &skalev1alpha1.PredictiveScalingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-policy", Namespace: "payments"},
		Spec: skalev1alpha1.PredictiveScalingPolicySpec{
			TargetRef: skalev1alpha1.TargetReference{Name: "checkout-api"},
		},
	}
	reader := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testDeployment("payments", "checkout-api"), testHPA("payments", "checkout-hpa", "checkout-api"), policy).
		Build()

	inventory, err := Scanner{
		Reader:                       reader,
		MetricsProvider:              staticProvider{snapshot: discoverySnapshot(now.Add(-2*time.Hour), true, []float64{120, 120, 120, 260, 300, 260})},
		ForecastModel:                staticForecast{result: forecastResult(now, 0.86, forecast.ReliabilityHigh)},
		Now:                          func() time.Time { return now },
		Lookback:                     2 * time.Hour,
		ExpectedResolution:           5 * time.Minute,
		IncludeDeploymentsWithoutHPA: true,
	}.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	finding := onlyFinding(t, inventory, "payments", "checkout-api")
	if finding.ExistingPolicy == nil || finding.ExistingPolicy.Name != "checkout-policy" {
		t.Fatalf("unexpected existing policy %#v", finding.ExistingPolicy)
	}
	if inventory.Summary.PolicyBacked != 1 {
		t.Fatalf("policyBacked = %d, want 1", inventory.Summary.PolicyBacked)
	}
	if strings.Contains(strings.ToLower(finding.PolicyDraft), "recommendedreplicas") {
		t.Fatalf("discovery policy draft should not contain recommendation status:\n%s", finding.PolicyDraft)
	}
}

type staticProvider struct {
	snapshot metrics.Snapshot
}

func (p staticProvider) LoadWindow(_ context.Context, _ metrics.Target, window metrics.Window) (metrics.Snapshot, error) {
	snapshot := p.snapshot
	snapshot.Window = window
	return snapshot, nil
}

type failingProvider struct {
	err error
}

func (p failingProvider) LoadWindow(context.Context, metrics.Target, metrics.Window) (metrics.Snapshot, error) {
	return metrics.Snapshot{}, p.err
}

type staticForecast struct {
	result forecast.Result
	err    error
}

func (m staticForecast) Name() string {
	return "static"
}

func (m staticForecast) Forecast(context.Context, forecast.Input) (forecast.Result, error) {
	return m.result, m.err
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(clientgoscheme) error = %v", err)
	}
	if err := autoscalingv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(autoscalingv2) error = %v", err)
	}
	if err := skalev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(skalev1alpha1) error = %v", err)
	}
	return scheme
}

func testDeployment(namespace, name string) *appsv1.Deployment {
	replicas := int32(3)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "app",
						Image: "example.invalid/app:latest",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
						},
					}},
				},
			},
		},
	}
}

func testStatefulSet(namespace, name string) *appsv1.StatefulSet {
	replicas := int32(2)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
		},
	}
}

func testDaemonSet(namespace, name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	}
}

func testJob(namespace, name string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	}
}

func testCronJob(namespace, name string) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	}
}

func testHPA(namespace, name, target string) *autoscalingv2.HorizontalPodAutoscaler {
	minReplicas := int32(2)
	utilization := int32(70)
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       target,
			},
			MinReplicas: &minReplicas,
			MaxReplicas: 8,
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name: corev1.ResourceCPU,
					Target: autoscalingv2.MetricTarget{
						Type:               autoscalingv2.UtilizationMetricType,
						AverageUtilization: &utilization,
					},
				},
			}},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			CurrentReplicas: 3,
		},
	}
}

func testStatefulSetHPA(namespace, name, target string) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
				Name:       target,
			},
			MaxReplicas: 8,
		},
	}
}

func discoverySnapshot(start time.Time, includeWarmup bool, pattern []float64) metrics.Snapshot {
	step := 5 * time.Minute
	values := repeat(pattern, int((2*time.Hour)/step)/len(pattern)+1)
	values = values[:int((2*time.Hour)/step)+1]
	replicas := make([]float64, len(values))
	cpu := make([]float64, len(values))
	memory := make([]float64, len(values))
	warmup := make([]float64, len(values))
	for i, value := range values {
		replicas[i] = 3
		cpu[i] = 0.45 + value/1000
		if cpu[i] > 0.9 {
			cpu[i] = 0.9
		}
		memory[i] = 0.55
		warmup[i] = 45
	}
	snapshot := metrics.Snapshot{
		Window:   metrics.Window{Start: start, End: start.Add(time.Duration(len(values)-1) * step)},
		Demand:   series(metrics.SignalDemand, start, step, values),
		Replicas: series(metrics.SignalReplicas, start, step, replicas),
		CPU:      ptrSeries(series(metrics.SignalCPU, start, step, cpu)),
		Memory:   ptrSeries(series(metrics.SignalMemory, start, step, memory)),
	}
	if includeWarmup {
		snapshot.Warmup = ptrSeries(series(metrics.SignalWarmup, start, step, warmup))
	}
	return snapshot
}

func forecastResult(now time.Time, confidence float64, reliability forecast.ReliabilityLevel) forecast.Result {
	return forecast.Result{
		Model:       forecast.SeasonalNaiveModelName,
		GeneratedAt: now,
		Horizon:     5 * time.Minute,
		Step:        5 * time.Minute,
		Points: []forecast.Point{{
			Timestamp: now.Add(5 * time.Minute),
			Value:     280,
		}},
		Confidence:  confidence,
		Reliability: reliability,
	}
}

func series(name metrics.SignalName, start time.Time, step time.Duration, values []float64) metrics.SignalSeries {
	samples := make([]metrics.Sample, 0, len(values))
	for i, value := range values {
		samples = append(samples, metrics.Sample{
			Timestamp: start.Add(time.Duration(i) * step),
			Value:     value,
		})
	}
	return metrics.SignalSeries{
		Name:                    name,
		ObservedLabelSignatures: []string{"namespace=payments,deployment=checkout-api"},
		Samples:                 samples,
	}
}

func repeat(pattern []float64, times int) []float64 {
	out := make([]float64, 0, len(pattern)*times)
	for i := 0; i < times; i++ {
		out = append(out, pattern...)
	}
	return out
}

func ptrSeries(series metrics.SignalSeries) *metrics.SignalSeries {
	return &series
}

func onlyFinding(t *testing.T, inventory Inventory, namespace, name string) Finding {
	t.Helper()
	for _, finding := range inventory.Findings {
		if finding.Workload.Namespace == namespace && finding.Workload.Name == name {
			return finding
		}
	}
	t.Fatalf("finding %s/%s not found in %#v", namespace, name, inventory.Findings)
	return Finding{}
}

func containsReason(finding Finding, code string) bool {
	for _, reason := range finding.Reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

var _ client.Reader = fake.NewClientBuilder().Build()
