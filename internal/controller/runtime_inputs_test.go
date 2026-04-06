package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/explain"
	"github.com/oswalpalash/skale/internal/metrics"
	prommetrics "github.com/oswalpalash/skale/internal/metrics/prometheus"
	"github.com/oswalpalash/skale/internal/safety"
)

func TestPrometheusDependencyEvaluatorUsesLatestSample(t *testing.T) {
	t.Parallel()

	evaluator := PrometheusDependencyEvaluator{
		API: dependencyAPI{
			results: map[string]prommetrics.RangeQueryResult{
				`avg(search_health_ratio{namespace="payments",deployment="checkout-api"})`: {
					Series: []prommetrics.QuerySeries{{
						Samples: []metrics.Sample{
							{Timestamp: time.Date(2026, time.April, 2, 0, 19, 0, 0, time.UTC), Value: 0.50},
							{Timestamp: time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC), Value: 0.97},
						},
					}},
				},
			},
		},
		QueryLookback: time.Minute,
		Step:          30 * time.Second,
	}

	statuses, err := evaluator.Evaluate(
		context.Background(),
		metrics.Target{Namespace: "payments", Name: "checkout-api"},
		[]skalev1alpha1.DependencyHealthCheck{{
			Name:            "search",
			Query:           `avg(search_health_ratio{namespace="$namespace",deployment="$deployment"})`,
			MinHealthyRatio: 0.95,
		}},
		time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("status count = %d, want 1", len(statuses))
	}
	if !statuses[0].Healthy {
		t.Fatalf("expected healthy dependency status, got %#v", statuses[0])
	}
	if statuses[0].HealthyRatio != 0.97 {
		t.Fatalf("healthy ratio = %.2f, want 0.97", statuses[0].HealthyRatio)
	}
}

func TestPrometheusDependencyEvaluatorFailsClosedOnQueryError(t *testing.T) {
	t.Parallel()

	evaluator := PrometheusDependencyEvaluator{
		API: dependencyAPI{err: errors.New("prometheus unavailable")},
	}

	statuses, err := evaluator.Evaluate(
		context.Background(),
		metrics.Target{Namespace: "payments", Name: "checkout-api"},
		[]skalev1alpha1.DependencyHealthCheck{{
			Name:            "search",
			Query:           `up`,
			MinHealthyRatio: 0.95,
		}},
		time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("status count = %d, want 1", len(statuses))
	}
	if statuses[0].Healthy {
		t.Fatalf("expected unhealthy dependency status on query error, got %#v", statuses[0])
	}
}

func TestKubernetesHeadroomProviderBuildsRequestBasedSnapshot(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(clientgoscheme) error = %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(appsv1) error = %v", err)
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "payments",
			Name:      "checkout-api",
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "app",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resourceMustParse("250m"),
								corev1.ResourceMemory: resourceMustParse("256Mi"),
							},
						},
					}},
				},
			},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resourceMustParse("4"),
				corev1.ResourceMemory: resourceMustParse("8Gi"),
			},
			Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "payments",
			Name:      "checkout-api-0",
		},
		Spec: corev1.PodSpec{
			NodeName: "node-a",
			Containers: []corev1.Container{{
				Name: "app",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resourceMustParse("500m"),
						corev1.ResourceMemory: resourceMustParse("512Mi"),
					},
				},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, node, pod).Build()
	provider := KubernetesHeadroomProvider{Reader: reader}
	observedAt := time.Date(2026, time.April, 2, 0, 20, 0, 0, time.UTC)

	signal, err := provider.HeadroomFor(context.Background(), ResolvedTarget{
		Identity: explain.WorkloadIdentity{
			Namespace: "payments",
			Name:      "checkout-api",
		},
	}, observedAt)
	if err != nil {
		t.Fatalf("HeadroomFor() error = %v", err)
	}
	if signal == nil {
		t.Fatal("expected headroom signal")
	}
	if signal.State != safety.NodeHeadroomStateReady {
		t.Fatalf("state = %q, want %q", signal.State, safety.NodeHeadroomStateReady)
	}
	if signal.PodRequests.CPUMilli != 250 || signal.PodRequests.MemoryBytes == 0 {
		t.Fatalf("unexpected pod requests %#v", signal.PodRequests)
	}
	if signal.ClusterSummary.Requested.CPUMilli != 500 {
		t.Fatalf("cluster requested CPU = %d, want 500", signal.ClusterSummary.Requested.CPUMilli)
	}
	if len(signal.Nodes) != 1 || !signal.Nodes[0].Schedulable {
		t.Fatalf("unexpected node summaries %#v", signal.Nodes)
	}
}

type dependencyAPI struct {
	results map[string]prommetrics.RangeQueryResult
	err     error
}

func (a dependencyAPI) QueryRange(_ context.Context, query string, _ time.Time, _ time.Time, _ time.Duration) (prommetrics.RangeQueryResult, error) {
	if a.err != nil {
		return prommetrics.RangeQueryResult{}, a.err
	}
	if result, ok := a.results[query]; ok {
		return result, nil
	}
	return prommetrics.RangeQueryResult{}, nil
}

func resourceMustParse(value string) resource.Quantity {
	return resource.MustParse(value)
}
