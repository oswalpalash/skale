package controller

import (
	"testing"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
)

func TestRecommendationMetricDisplayableRequiresReadyTelemetry(t *testing.T) {
	t.Parallel()

	recommendation := &skalev1alpha1.RecommendationSummary{
		State:               skalev1alpha1.RecommendationStateAvailable,
		RecommendedReplicas: 6,
	}
	status := skalev1alpha1.PredictiveScalingPolicyStatus{
		TelemetryReadiness: &skalev1alpha1.TelemetryReadinessSummary{
			State: skalev1alpha1.TelemetryReadinessStateDegraded,
		},
		LastRecommendation: recommendation,
	}
	if recommendationMetricDisplayable(status, recommendation) {
		t.Fatal("degraded telemetry should not publish surfaced recommendation replicas")
	}

	status.TelemetryReadiness.State = skalev1alpha1.TelemetryReadinessStateReady
	if !recommendationMetricDisplayable(status, recommendation) {
		t.Fatal("ready telemetry with available recommendation should publish surfaced recommendation replicas")
	}

	status.SuppressionReasons = []skalev1alpha1.SuppressionReason{{Code: "telemetry_not_ready"}}
	if recommendationMetricDisplayable(status, recommendation) {
		t.Fatal("telemetry_not_ready suppression should not publish surfaced recommendation replicas")
	}
}
