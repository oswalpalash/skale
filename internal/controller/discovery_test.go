package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/discovery"
	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/metrics"
)

func TestClusterDiscoveryRunnerPublishesInventoryConfigMap(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	scheme := discoveryTestScheme(t)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "skale-system"}},
			discoveryTestDeployment("payments", "checkout-api"),
			discoveryTestHPA("payments", "checkout-hpa", "checkout-api"),
		).
		Build()

	runner := ClusterDiscoveryRunner{
		Client: k8sClient,
		Scanner: discovery.Scanner{
			Reader:                       k8sClient,
			MetricsProvider:              controllerDiscoveryProvider{snapshot: controllerDiscoverySnapshot(now.Add(-2 * time.Hour))},
			ForecastModel:                controllerDiscoveryForecast{result: controllerForecastResult(now)},
			Now:                          func() time.Time { return now },
			Lookback:                     2 * time.Hour,
			ExpectedResolution:           5 * time.Minute,
			IncludeDeploymentsWithoutHPA: true,
		},
		Namespace:       "skale-system",
		ConfigMapName:   "skale-discovery-inventory",
		PublishPolicies: true,
	}

	if err := runner.scanAndPublish(context.Background()); err != nil {
		t.Fatalf("scanAndPublish() error = %v", err)
	}

	var configMap corev1.ConfigMap
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "skale-system", Name: "skale-discovery-inventory"}, &configMap); err != nil {
		t.Fatalf("Get(ConfigMap) error = %v", err)
	}
	if configMap.Labels["skale.io/discovery-inventory"] != "true" {
		t.Fatalf("unexpected labels %#v", configMap.Labels)
	}
	if !strings.Contains(configMap.Data["summary.txt"], "candidate: 1") {
		t.Fatalf("unexpected summary:\n%s", configMap.Data["summary.txt"])
	}
	if !strings.Contains(configMap.Data["policy-drafts.yaml"], "kind: PredictiveScalingPolicy") {
		t.Fatalf("expected policy draft, got:\n%s", configMap.Data["policy-drafts.yaml"])
	}

	var inventory discovery.Inventory
	if err := json.Unmarshal([]byte(configMap.Data["inventory.json"]), &inventory); err != nil {
		t.Fatalf("unmarshal inventory: %v", err)
	}
	if inventory.Summary.Candidates != 1 || len(inventory.Findings) != 1 {
		t.Fatalf("unexpected inventory summary=%#v findings=%#v", inventory.Summary, inventory.Findings)
	}
	if inventory.Findings[0].Status != discovery.StatusCandidate {
		t.Fatalf("finding status = %q, want candidate", inventory.Findings[0].Status)
	}
}

func discoveryTestScheme(t *testing.T) *runtime.Scheme {
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

func discoveryTestDeployment(namespace, name string) *appsv1.Deployment {
	replicas := int32(3)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
	}
}

func discoveryTestHPA(namespace, name, target string) *autoscalingv2.HorizontalPodAutoscaler {
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
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 3},
	}
}

type controllerDiscoveryProvider struct {
	snapshot metrics.Snapshot
}

func (p controllerDiscoveryProvider) LoadWindow(_ context.Context, _ metrics.Target, window metrics.Window) (metrics.Snapshot, error) {
	snapshot := p.snapshot
	snapshot.Window = window
	return snapshot, nil
}

type controllerDiscoveryForecast struct {
	result forecast.Result
}

func (m controllerDiscoveryForecast) Name() string {
	return "controller-discovery"
}

func (m controllerDiscoveryForecast) Forecast(context.Context, forecast.Input) (forecast.Result, error) {
	return m.result, nil
}

func controllerDiscoverySnapshot(start time.Time) metrics.Snapshot {
	step := 5 * time.Minute
	pattern := []float64{120, 120, 120, 260, 300, 260}
	values := make([]float64, 0, 25)
	for len(values) < 25 {
		values = append(values, pattern...)
	}
	values = values[:25]
	replicas := make([]float64, len(values))
	cpu := make([]float64, len(values))
	memory := make([]float64, len(values))
	warmup := make([]float64, len(values))
	for i, value := range values {
		replicas[i] = 3
		cpu[i] = 0.45 + value/1000
		memory[i] = 0.55
		warmup[i] = 45
	}
	return metrics.Snapshot{
		Window:   metrics.Window{Start: start, End: start.Add(time.Duration(len(values)-1) * step)},
		Demand:   controllerSeries(metrics.SignalDemand, start, step, values),
		Replicas: controllerSeries(metrics.SignalReplicas, start, step, replicas),
		CPU:      controllerSeriesPtr(controllerSeries(metrics.SignalCPU, start, step, cpu)),
		Memory:   controllerSeriesPtr(controllerSeries(metrics.SignalMemory, start, step, memory)),
		Warmup:   controllerSeriesPtr(controllerSeries(metrics.SignalWarmup, start, step, warmup)),
	}
}

func controllerForecastResult(now time.Time) forecast.Result {
	return forecast.Result{
		Model:       forecast.SeasonalNaiveModelName,
		GeneratedAt: now,
		Horizon:     5 * time.Minute,
		Step:        5 * time.Minute,
		Points: []forecast.Point{{
			Timestamp: now.Add(5 * time.Minute),
			Value:     280,
		}},
		Confidence:  0.86,
		Reliability: forecast.ReliabilityHigh,
	}
}

func controllerSeries(name metrics.SignalName, start time.Time, step time.Duration, values []float64) metrics.SignalSeries {
	samples := make([]metrics.Sample, 0, len(values))
	for i, value := range values {
		samples = append(samples, metrics.Sample{Timestamp: start.Add(time.Duration(i) * step), Value: value})
	}
	return metrics.SignalSeries{
		Name:                    name,
		ObservedLabelSignatures: []string{"namespace=payments,deployment=checkout-api"},
		Samples:                 samples,
	}
}

func controllerSeriesPtr(series metrics.SignalSeries) *metrics.SignalSeries {
	return &series
}
