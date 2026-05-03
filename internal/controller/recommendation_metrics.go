package controller

import (
	"strconv"

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
			Help: "Surfaced recommended replicas from the last Skale recommendation evaluation. The series is omitted while telemetry is still learning or unavailable.",
		},
		[]string{"namespace", "workload", "policy", "state"},
	)
	forecastPredictedReplicas = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skale_forecast_predicted_replicas",
			Help: "Model predicted replicas from the last Skale forecast evaluation. Prometheus scrape history provides the historical prediction timeline.",
		},
		[]string{"namespace", "workload", "policy", "model", "horizon", "selected"},
	)
	forecastPredictedDemand = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skale_forecast_predicted_demand",
			Help: "Model predicted normalized demand from the last Skale forecast evaluation.",
		},
		[]string{"namespace", "workload", "policy", "model", "horizon", "selected"},
	)
	forecastConfidence = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skale_forecast_confidence",
			Help: "Model confidence from the last Skale forecast evaluation.",
		},
		[]string{"namespace", "workload", "policy", "model", "selected"},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		recommendationCurrentReplicas,
		recommendationRecommendedReplicas,
		forecastPredictedReplicas,
		forecastPredictedDemand,
		forecastConfidence,
	)
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
	clearRecommendedReplicaStates(policy.Namespace, workload, policy.Name)
	value := prometheus.Labels{
		"namespace": policy.Namespace,
		"workload":  workload,
		"policy":    policy.Name,
		"state":     state,
	}
	if recommendationMetricDisplayable(policy.Status, recommendation) {
		recommendationRecommendedReplicas.With(value).Set(float64(recommendation.RecommendedReplicas))
	}
}

func recommendationMetricDisplayable(status skalev1alpha1.PredictiveScalingPolicyStatus, recommendation *skalev1alpha1.RecommendationSummary) bool {
	if recommendation == nil {
		return false
	}
	if status.TelemetryReadiness == nil || status.TelemetryReadiness.State != skalev1alpha1.TelemetryReadinessStateReady {
		return false
	}
	for _, reason := range status.SuppressionReasons {
		if reason.Code == "telemetry_not_ready" {
			return false
		}
	}
	return recommendation.State == skalev1alpha1.RecommendationStateAvailable ||
		recommendation.State == skalev1alpha1.RecommendationStateSuppressed
}

func clearRecommendedReplicaStates(namespace, workload, policy string) {
	for _, state := range []string{"available", "suppressed", "unavailable", "unknown"} {
		recommendationRecommendedReplicas.DeleteLabelValues(namespace, workload, policy, state)
	}
}

func publishForecastMetrics(policy skalev1alpha1.PredictiveScalingPolicy, evaluation LiveEvaluation) {
	workload := policy.Spec.TargetRef.Name
	if observed := policy.Status.ObservedWorkload; observed != nil && observed.Name != "" {
		workload = observed.Name
	}
	clearForecastMetrics(policy.Namespace, workload, policy.Name)
	for _, prediction := range evaluation.ForecastMetrics {
		if prediction.Model == "" || prediction.Horizon == "" || prediction.Error != "" || prediction.Reliability == "unsupported" {
			continue
		}
		selected := strconv.FormatBool(prediction.Selected)
		labels := prometheus.Labels{
			"namespace": policy.Namespace,
			"workload":  workload,
			"policy":    policy.Name,
			"model":     prediction.Model,
			"horizon":   prediction.Horizon,
			"selected":  selected,
		}
		forecastPredictedDemand.With(labels).Set(prediction.Demand)
		if prediction.HasReplicas {
			forecastPredictedReplicas.With(labels).Set(float64(prediction.Replicas))
		}
		forecastConfidence.With(prometheus.Labels{
			"namespace": policy.Namespace,
			"workload":  workload,
			"policy":    policy.Name,
			"model":     prediction.Model,
			"selected":  selected,
		}).Set(prediction.Confidence)
	}
}

func clearForecastMetrics(namespace, workload, policy string) {
	for _, model := range []string{"timesfm", "seasonal_naive", "holt_winters", "auto", "side_by_side"} {
		for _, selected := range []string{"true", "false"} {
			forecastPredictedReplicas.DeleteLabelValues(namespace, workload, policy, model, "ready", selected)
			forecastPredictedDemand.DeleteLabelValues(namespace, workload, policy, model, "ready", selected)
			forecastConfidence.DeleteLabelValues(namespace, workload, policy, model, selected)
		}
	}
}
