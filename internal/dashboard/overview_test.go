package dashboard

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/discovery"
)

func TestBuildOverviewSeparatesQualificationFromRecommendations(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	policy := skalev1alpha1.PredictiveScalingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-policy", Namespace: "payments", Generation: 7},
		Spec: skalev1alpha1.PredictiveScalingPolicySpec{
			TargetRef: skalev1alpha1.TargetReference{Name: "checkout-api"},
			Mode:      skalev1alpha1.PredictiveScalingModeRecommendationOnly,
		},
		Status: skalev1alpha1.PredictiveScalingPolicyStatus{
			TelemetryReadiness: &skalev1alpha1.TelemetryReadinessSummary{
				State:   skalev1alpha1.TelemetryReadinessStateReady,
				Message: "telemetry ready",
			},
			LastForecast: &skalev1alpha1.ForecastSummary{
				Method:     "seasonal_naive",
				Confidence: 0.82,
			},
			LastRecommendation: &skalev1alpha1.RecommendationSummary{
				State:               skalev1alpha1.RecommendationStateAvailable,
				BaselineReplicas:    3,
				RecommendedReplicas: 5,
			},
		},
	}

	overview := BuildOverview(discovery.Inventory{
		GeneratedAt: now.Add(-time.Minute),
		Findings: []discovery.Finding{
			{
				Status: discovery.StatusCandidate,
				Workload: discovery.WorkloadRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Namespace:  "payments",
					Name:       "checkout-api",
				},
				HPA: &discovery.HPASummary{Name: "checkout-hpa", CurrentReplicas: 3},
			},
			{
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
			},
		},
	}, []skalev1alpha1.PredictiveScalingPolicy{policy}, now)

	if overview.Summary.Total != 2 {
		t.Fatalf("total = %d, want 2", overview.Summary.Total)
	}
	if overview.Summary.PolicyBacked != 1 || overview.Summary.NeedsScalingContract != 1 {
		t.Fatalf("unexpected summary %#v", overview.Summary)
	}
	checkout := findWorkload(t, overview, "payments/checkout-api")
	if checkout.Qualification != QualificationPolicyBacked {
		t.Fatalf("checkout qualification = %q, want %q", checkout.Qualification, QualificationPolicyBacked)
	}
	if checkout.RecommendedReplicas == nil || *checkout.RecommendedReplicas != 5 {
		t.Fatalf("checkout recommended replicas = %#v, want 5", checkout.RecommendedReplicas)
	}
	ledger := findWorkload(t, overview, "payments/ledger-api")
	if ledger.Qualification != string(discovery.StatusNeedsScalingContract) {
		t.Fatalf("ledger qualification = %q, want needs scaling contract", ledger.Qualification)
	}
	if ledger.RecommendedReplicas != nil {
		t.Fatalf("ledger should not have recommended replicas, got %#v", ledger.RecommendedReplicas)
	}
	if ledger.NextAction == "" || !strings.Contains(ledger.NextAction, "scaling contract") {
		t.Fatalf("ledger next action = %q", ledger.NextAction)
	}
}

func TestBuildOverviewKeepsObservedHPAReplicasOverStaleRecommendationBaseline(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	policy := skalev1alpha1.PredictiveScalingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-policy", Namespace: "payments", Generation: 7},
		Spec: skalev1alpha1.PredictiveScalingPolicySpec{
			TargetRef: skalev1alpha1.TargetReference{Name: "checkout-api"},
			Mode:      skalev1alpha1.PredictiveScalingModeRecommendationOnly,
		},
		Status: skalev1alpha1.PredictiveScalingPolicyStatus{
			LastRecommendation: &skalev1alpha1.RecommendationSummary{
				State:            skalev1alpha1.RecommendationStateUnavailable,
				BaselineReplicas: 2,
			},
		},
	}

	overview := BuildOverview(discovery.Inventory{
		GeneratedAt: now.Add(-time.Minute),
		Findings: []discovery.Finding{{
			Status: discovery.StatusCandidate,
			Workload: discovery.WorkloadRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Namespace:  "payments",
				Name:       "checkout-api",
			},
			HPA: &discovery.HPASummary{Name: "checkout-hpa", CurrentReplicas: 6},
		}},
	}, []skalev1alpha1.PredictiveScalingPolicy{policy}, now)

	checkout := findWorkload(t, overview, "payments/checkout-api")
	if checkout.CurrentReplicas == nil || *checkout.CurrentReplicas != 6 {
		t.Fatalf("current replicas = %#v, want observed HPA value 6", checkout.CurrentReplicas)
	}
	if checkout.RecommendedReplicas != nil {
		t.Fatalf("unavailable recommendation should not set recommended replicas, got %#v", checkout.RecommendedReplicas)
	}
}

func TestRenderHTMLIncludesQualificationConsole(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 3, 12, 0, 0, 0, time.UTC)
	output, err := RenderHTML(Overview{
		GeneratedAt: now,
		InventoryAt: now,
		Summary: Summary{
			Total:                1,
			NeedsScalingContract: 1,
		},
		Workloads: []Workload{{
			ID:                   "payments/ledger-api",
			APIVersion:           "apps/v1",
			Kind:                 "Deployment",
			Namespace:            "payments",
			Name:                 "ledger-api",
			Qualification:        string(discovery.StatusNeedsScalingContract),
			ScalingContract:      ScalingContractMissing,
			MissingPrerequisites: []string{"scaling contract"},
			NextAction:           "add an HPA or explicit Skale scaling contract before recommendations",
		}},
	})
	if err != nil {
		t.Fatalf("RenderHTML() error = %v", err)
	}
	text := string(output)
	for _, expected := range []string{
		"Workload Qualification Console",
		"payments/ledger-api",
		"needs scaling contract",
		"add an HPA or explicit Skale scaling contract",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in HTML:\n%s", expected, text)
		}
	}
}

func findWorkload(t *testing.T, overview Overview, id string) Workload {
	t.Helper()

	for _, workload := range overview.Workloads {
		if workload.ID == id {
			return workload
		}
	}
	t.Fatalf("workload %q not found in %#v", id, overview.Workloads)
	return Workload{}
}
