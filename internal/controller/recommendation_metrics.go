package controller

import (
	"math"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
)

var (
	recommendationCurrentReplicas = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skale_recommendation_current_replicas",
			Help: "Current baseline replicas observed during the last Skale recommendation evaluation.",
		},
		[]string{"namespace", "workload", "policy"},
	)
	recommendationRecommendedReplicas = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skale_recommendation_recommended_replicas",
			Help: "Recommended replicas from the last Skale recommendation evaluation. Set to NaN when unavailable.",
		},
		[]string{"namespace", "workload", "policy", "state"},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(recommendationCurrentReplicas, recommendationRecommendedReplicas)
}

func publishRecommendationMetrics(policy skalev1alpha1.PredictiveScalingPolicy) {
	workload := policy.Spec.TargetRef.Name
	if observed := policy.Status.ObservedWorkload; observed != nil && observed.Name != "" {
		workload = observed.Name
	}
	labels := prometheus.Labels{
		"namespace": policy.Namespace,
		"workload":  workload,
		"policy":    policy.Name,
	}

	recommendation := policy.Status.LastRecommendation
	if recommendation == nil {
		return
	}
	if recommendation.BaselineReplicas > 0 {
		recommendationCurrentReplicas.With(labels).Set(float64(recommendation.BaselineReplicas))
	}

	state := string(recommendation.State)
	if state == "" {
		state = "unknown"
	}
	value := prometheus.Labels{
		"namespace": policy.Namespace,
		"workload":  workload,
		"policy":    policy.Name,
		"state":     state,
	}
	if recommendation.State == skalev1alpha1.RecommendationStateAvailable ||
		recommendation.State == skalev1alpha1.RecommendationStateSuppressed {
		recommendationRecommendedReplicas.With(value).Set(float64(recommendation.RecommendedReplicas))
		return
	}
	recommendationRecommendedReplicas.With(value).Set(math.NaN())
}
