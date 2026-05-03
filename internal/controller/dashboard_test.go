package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/dashboard"
	"github.com/oswalpalash/skale/internal/discovery"
	"github.com/oswalpalash/skale/internal/metrics"
)

func TestDashboardServerServesOverviewAPI(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	inventory := discovery.Inventory{
		GeneratedAt: now.Add(-time.Minute),
		Findings: []discovery.Finding{{
			Status: discovery.StatusNeedsScalingContract,
			Workload: discovery.WorkloadRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Namespace:  "payments",
				Name:       "ledger-api",
			},
			MissingPrerequisites: []string{"scaling contract"},
			Reasons: []discovery.Reason{{
				Code:    discovery.ReasonScalingContractMissing,
				Message: "missing scaling contract",
			}},
		}},
	}
	payload, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("marshal inventory: %v", err)
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(discoveryTestScheme(t)).
		WithObjects(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "skale-system", Name: "skale-discovery-inventory"},
			Data:       map[string]string{"inventory.json": string(payload)},
		}).
		Build()
	server := DashboardServer{
		Client:        k8sClient,
		Namespace:     "skale-system",
		ConfigMapName: "skale-discovery-inventory",
		Now:           func() time.Time { return now },
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	server.handleOverview(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var overview dashboard.Overview
	if err := json.Unmarshal(recorder.Body.Bytes(), &overview); err != nil {
		t.Fatalf("unmarshal overview: %v", err)
	}
	if overview.Summary.NeedsScalingContract != 1 {
		t.Fatalf("needsScalingContract = %d, want 1", overview.Summary.NeedsScalingContract)
	}
	if got := overview.Workloads[0].RecommendedReplicas; got != nil {
		t.Fatalf("non-policy workload should not have recommendation, got %#v", got)
	}
}

func TestDashboardServerServesTimelineAPI(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	evaluatedAt := metav1.Time{Time: now.Add(-time.Minute)}
	k8sClient := fake.NewClientBuilder().
		WithScheme(discoveryTestScheme(t)).
		WithObjects(&skalev1alpha1.PredictiveScalingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "checkout-policy", Namespace: "payments"},
			Spec: skalev1alpha1.PredictiveScalingPolicySpec{
				TargetRef: skalev1alpha1.TargetReference{Name: "checkout-api"},
			},
			Status: skalev1alpha1.PredictiveScalingPolicyStatus{
				TelemetryReadiness: &skalev1alpha1.TelemetryReadinessSummary{
					State: skalev1alpha1.TelemetryReadinessStateReady,
				},
				LastRecommendation: &skalev1alpha1.RecommendationSummary{
					State:               skalev1alpha1.RecommendationStateAvailable,
					EvaluatedAt:         &evaluatedAt,
					RecommendedReplicas: 5,
				},
			},
		}).
		Build()
	server := DashboardServer{
		Client: k8sClient,
		Metrics: dashboardMetricsProvider{
			snapshot: metrics.Snapshot{
				Demand: metrics.SignalSeries{
					Name: metrics.SignalDemand,
					Samples: []metrics.Sample{
						{Timestamp: now.Add(-2 * time.Minute), Value: 12},
						{Timestamp: now.Add(-time.Minute), Value: 30},
					},
				},
				Replicas: metrics.SignalSeries{
					Name: metrics.SignalReplicas,
					Samples: []metrics.Sample{
						{Timestamp: now.Add(-2 * time.Minute), Value: 3},
						{Timestamp: now.Add(-time.Minute), Value: 4},
					},
				},
				CPU: &metrics.SignalSeries{
					Name: metrics.SignalCPU,
					Samples: []metrics.Sample{
						{Timestamp: now.Add(-2 * time.Minute), Value: 0.4},
						{Timestamp: now.Add(-time.Minute), Value: 0.9},
					},
				},
			},
		},
		Now: func() time.Time { return now },
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/workloads/payments/checkout-api/timeline?lookback=5m", nil)
	server.handleTimeline(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var timeline dashboard.Timeline
	if err := json.Unmarshal(recorder.Body.Bytes(), &timeline); err != nil {
		t.Fatalf("unmarshal timeline: %v", err)
	}
	if len(timeline.Samples) != 2 {
		t.Fatalf("samples = %d, want 2: %#v", len(timeline.Samples), timeline.Samples)
	}
	if len(timeline.Demand) != 2 || timeline.Demand[1].Value != 30 {
		t.Fatalf("demand = %#v, want two demand samples", timeline.Demand)
	}
	if len(timeline.CPU) != 2 || timeline.CPU[1].Value != 0.9 {
		t.Fatalf("cpu = %#v, want two cpu samples", timeline.CPU)
	}
	if timeline.Recommendation == nil || timeline.Recommendation.Replicas != 5 {
		t.Fatalf("recommendation = %#v, want 5 replicas", timeline.Recommendation)
	}
}

func TestDashboardServerServesHistoricalTimelineRecommendations(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	k8sClient := fake.NewClientBuilder().
		WithScheme(discoveryTestScheme(t)).
		WithObjects(&skalev1alpha1.PredictiveScalingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "checkout-policy", Namespace: "payments"},
			Spec: skalev1alpha1.PredictiveScalingPolicySpec{
				TargetRef: skalev1alpha1.TargetReference{Name: "checkout-api"},
			},
			Status: skalev1alpha1.PredictiveScalingPolicyStatus{
				TelemetryReadiness: &skalev1alpha1.TelemetryReadinessSummary{
					State: skalev1alpha1.TelemetryReadinessStateReady,
				},
				LastRecommendation: &skalev1alpha1.RecommendationSummary{
					State:               skalev1alpha1.RecommendationStateAvailable,
					RecommendedReplicas: 7,
				},
			},
		}).
		Build()
	server := DashboardServer{
		Client: k8sClient,
		Metrics: dashboardMetricsProvider{
			snapshot: metrics.Snapshot{
				Replicas: metrics.SignalSeries{
					Name: metrics.SignalReplicas,
					Samples: []metrics.Sample{
						{Timestamp: now.Add(-3 * time.Minute), Value: 3},
					},
				},
			},
			recommendationHistory: []metrics.RecommendationSample{{
				Timestamp: now.Add(-3 * time.Minute),
				Replicas:  4,
				State:     "available",
				Policy:    "checkout-policy",
			}, {
				Timestamp: now.Add(-2 * time.Minute),
				Replicas:  9,
				State:     "available",
				Policy:    "other-policy",
			}, {
				Timestamp: now.Add(-time.Minute),
				Replicas:  5,
				State:     "suppressed",
				Policy:    "checkout-policy",
			}},
		},
		Now: func() time.Time { return now },
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/workloads/payments/checkout-api/timeline?lookback=5m", nil)
	server.handleTimeline(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var timeline dashboard.Timeline
	if err := json.Unmarshal(recorder.Body.Bytes(), &timeline); err != nil {
		t.Fatalf("unmarshal timeline: %v", err)
	}
	if got, want := len(timeline.Recommendations), 2; got != want {
		t.Fatalf("recommendations = %d, want %d: %#v", got, want, timeline.Recommendations)
	}
	if timeline.Recommendation == nil || timeline.Recommendation.Replicas != 5 || timeline.Recommendation.State != "suppressed" {
		t.Fatalf("latest recommendation = %#v, want suppressed 5", timeline.Recommendation)
	}
}

func TestDashboardServerHidesTimelineRecommendationDuringTelemetryLearning(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	k8sClient := fake.NewClientBuilder().
		WithScheme(discoveryTestScheme(t)).
		WithObjects(&skalev1alpha1.PredictiveScalingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "checkout-policy", Namespace: "payments"},
			Spec: skalev1alpha1.PredictiveScalingPolicySpec{
				TargetRef: skalev1alpha1.TargetReference{Name: "checkout-api"},
			},
			Status: skalev1alpha1.PredictiveScalingPolicyStatus{
				TelemetryReadiness: &skalev1alpha1.TelemetryReadinessSummary{
					State: skalev1alpha1.TelemetryReadinessStateDegraded,
				},
				SuppressionReasons: []skalev1alpha1.SuppressionReason{{
					Code: "telemetry_not_ready",
				}},
				LastRecommendation: &skalev1alpha1.RecommendationSummary{
					State:               skalev1alpha1.RecommendationStateSuppressed,
					RecommendedReplicas: 5,
				},
			},
		}).
		Build()
	server := DashboardServer{
		Client: k8sClient,
		Metrics: dashboardMetricsProvider{snapshot: metrics.Snapshot{
			Replicas: metrics.SignalSeries{
				Name: metrics.SignalReplicas,
				Samples: []metrics.Sample{
					{Timestamp: now.Add(-time.Minute), Value: 4},
				},
			},
		}},
		Now: func() time.Time { return now },
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/workloads/payments/checkout-api/timeline?lookback=5m", nil)
	server.handleTimeline(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var timeline dashboard.Timeline
	if err := json.Unmarshal(recorder.Body.Bytes(), &timeline); err != nil {
		t.Fatalf("unmarshal timeline: %v", err)
	}
	if timeline.Recommendation != nil {
		t.Fatalf("learning-phase recommendation overlay = %#v, want nil", timeline.Recommendation)
	}
}

func TestDashboardServerDoesNotRequireLeaderElection(t *testing.T) {
	t.Parallel()

	server := DashboardServer{}
	if server.NeedLeaderElection() {
		t.Fatal("dashboard should be available before reconciliation leadership is acquired")
	}
}

func TestDashboardServerServesHTML(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	inventory := discovery.Inventory{
		GeneratedAt: now.Add(-time.Minute),
		Findings: []discovery.Finding{{
			Status: discovery.StatusCandidate,
			Workload: discovery.WorkloadRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Namespace:  "payments",
				Name:       "checkout-api",
			},
			HPA: &discovery.HPASummary{Name: "checkout-hpa", CurrentReplicas: 3},
		}},
	}
	payload, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("marshal inventory: %v", err)
	}

	policy := &skalev1alpha1.PredictiveScalingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-policy", Namespace: "payments"},
		Spec: skalev1alpha1.PredictiveScalingPolicySpec{
			TargetRef: skalev1alpha1.TargetReference{Name: "checkout-api"},
		},
		Status: skalev1alpha1.PredictiveScalingPolicyStatus{
			LastRecommendation: &skalev1alpha1.RecommendationSummary{
				State:               skalev1alpha1.RecommendationStateAvailable,
				BaselineReplicas:    3,
				RecommendedReplicas: 5,
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(discoveryTestScheme(t)).
		WithObjects(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "skale-system", Name: "skale-discovery-inventory"},
			Data:       map[string]string{"inventory.json": string(payload)},
		}, policy).
		Build()
	server := DashboardServer{
		Client:        k8sClient,
		Namespace:     "skale-system",
		ConfigMapName: "skale-discovery-inventory",
		Now:           func() time.Time { return now },
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	server.handleHTML(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	text := recorder.Body.String()
	for _, expected := range []string{
		"Workload Qualification Console",
		"Namespaces",
		"Replica timeline",
		"timeline-window",
		"lookbackOptions = ['30m', '1h', '3h', '6h']",
		"Demand and resource pressure",
		"overview-data",
		"payments/checkout-api",
		"policy-backed",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in HTML:\n%s", expected, text)
		}
	}
}

type dashboardMetricsProvider struct {
	snapshot              metrics.Snapshot
	err                   error
	recommendationHistory []metrics.RecommendationSample
}

func (p dashboardMetricsProvider) LoadWindow(context.Context, metrics.Target, metrics.Window) (metrics.Snapshot, error) {
	if p.err != nil {
		return metrics.Snapshot{}, p.err
	}
	return p.snapshot, nil
}

func (p dashboardMetricsProvider) LoadRecommendationHistory(context.Context, metrics.Target, metrics.Window) ([]metrics.RecommendationSample, error) {
	return p.recommendationHistory, nil
}
