package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPredictiveScalingPolicyDefaultSetsConservativeDefaults(t *testing.T) {
	t.Parallel()

	policy := &PredictiveScalingPolicy{
		Spec: PredictiveScalingPolicySpec{
			TargetRef: TargetReference{
				Name: "checkout-api",
			},
			MinReplicas: 2,
			MaxReplicas: 12,
			DependencyHealthChecks: []DependencyHealthCheck{
				{
					Name:            "payments",
					Query:           "avg_over_time(payments_ready_ratio[5m])",
					MinHealthyRatio: 0.95,
				},
			},
		},
	}

	policy.Default()

	if policy.Spec.TargetRef.APIVersion != "apps/v1" {
		t.Fatalf("expected targetRef.apiVersion default apps/v1, got %q", policy.Spec.TargetRef.APIVersion)
	}
	if policy.Spec.TargetRef.Kind != "Deployment" {
		t.Fatalf("expected targetRef.kind default Deployment, got %q", policy.Spec.TargetRef.Kind)
	}
	if policy.Spec.Mode != PredictiveScalingModeRecommendationOnly {
		t.Fatalf("expected mode default %q, got %q", PredictiveScalingModeRecommendationOnly, policy.Spec.Mode)
	}
	if policy.Spec.ForecastHorizon.Duration != defaultForecastHorizon {
		t.Fatalf("expected forecast horizon %s, got %s", defaultForecastHorizon, policy.Spec.ForecastHorizon.Duration)
	}
	if policy.Spec.Warmup.EstimatedReadyDuration.Duration != defaultWarmupDuration {
		t.Fatalf("expected warmup duration %s, got %s", defaultWarmupDuration, policy.Spec.Warmup.EstimatedReadyDuration.Duration)
	}
	if policy.Spec.CooldownWindow.Duration != defaultCooldownWindow {
		t.Fatalf("expected cooldown window %s, got %s", defaultCooldownWindow, policy.Spec.CooldownWindow.Duration)
	}
	if policy.Spec.ConfidenceThreshold != defaultConfidenceThreshold {
		t.Fatalf("expected confidence threshold %v, got %v", defaultConfidenceThreshold, policy.Spec.ConfidenceThreshold)
	}
	if policy.Spec.NodeHeadroomSanity != NodeHeadroomSanityRequireForScaleUp {
		t.Fatalf("expected node headroom sanity default %q, got %q", NodeHeadroomSanityRequireForScaleUp, policy.Spec.NodeHeadroomSanity)
	}
	if policy.Spec.DependencyHealthChecks[0].Type != DependencyHealthCheckTypePrometheusQuery {
		t.Fatalf("expected dependency check type default %q, got %q", DependencyHealthCheckTypePrometheusQuery, policy.Spec.DependencyHealthChecks[0].Type)
	}
}

func TestPredictiveScalingPolicyValidateAcceptsMinimalValidSpec(t *testing.T) {
	t.Parallel()

	policy := validPolicy()
	policy.Default()

	if errs := policy.Validate(); len(errs) != 0 {
		t.Fatalf("expected no validation errors, got %v", errs)
	}
}

func TestPredictiveScalingPolicyValidateRejectsInvalidBoundsAndTarget(t *testing.T) {
	t.Parallel()

	policy := validPolicy()
	policy.Spec.TargetRef.APIVersion = "batch/v1"
	policy.Spec.TargetRef.Kind = "CronJob"
	policy.Spec.MinReplicas = 10
	policy.Spec.MaxReplicas = 2
	policy.Spec.ForecastHorizon.Duration = 45 * time.Minute
	policy.Spec.Warmup.EstimatedReadyDuration.Duration = 0
	policy.Spec.ConfidenceThreshold = 1.2
	policy.Spec.ScaleUp = &ScaleStepPolicy{MaxReplicasChange: 0}
	policy.Spec.CooldownWindow.Duration = -1 * time.Minute

	errs := policy.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation errors")
	}

	expectedFields := map[string]bool{
		"spec.targetRef.apiVersion":          false,
		"spec.targetRef.kind":                false,
		"spec.forecastHorizon":               false,
		"spec.warmup.estimatedReadyDuration": false,
		"spec.confidenceThreshold":           false,
		"spec.minReplicas":                   false,
		"spec.scaleUp.maxReplicasChange":     false,
		"spec.cooldownWindow":                false,
	}
	for _, err := range errs {
		if _, ok := expectedFields[err.Field]; ok {
			expectedFields[err.Field] = true
		}
	}
	for fieldName, seen := range expectedFields {
		if !seen {
			t.Fatalf("expected validation error for %s, got %v", fieldName, errs)
		}
	}
}

func TestPredictiveScalingPolicyValidateRejectsInvalidWindowsAndDependencyChecks(t *testing.T) {
	t.Parallel()

	policy := validPolicy()
	start := metav1.NewTime(time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC))
	end := metav1.NewTime(start.Add(-10 * time.Minute))
	policy.Spec.BlackoutWindows = []BlackoutWindow{
		{
			Name:  "incident-window",
			Start: start,
			End:   end,
		},
	}
	policy.Spec.KnownEvents = []KnownEvent{
		{
			Name:  "product-launch",
			Start: start,
			End:   end,
		},
	}
	policy.Spec.DependencyHealthChecks = []DependencyHealthCheck{
		{
			Name:            "",
			Type:            "custom",
			Query:           "",
			MinHealthyRatio: 0,
		},
	}

	errs := policy.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation errors")
	}

	expectedFields := map[string]bool{
		"spec.blackoutWindows[0].end":                    false,
		"spec.knownEvents[0].end":                        false,
		"spec.dependencyHealthChecks[0].name":            false,
		"spec.dependencyHealthChecks[0].type":            false,
		"spec.dependencyHealthChecks[0].query":           false,
		"spec.dependencyHealthChecks[0].minHealthyRatio": false,
	}
	for _, err := range errs {
		if _, ok := expectedFields[err.Field]; ok {
			expectedFields[err.Field] = true
		}
	}
	for fieldName, seen := range expectedFields {
		if !seen {
			t.Fatalf("expected validation error for %s, got %v", fieldName, errs)
		}
	}
}

func validPolicy() *PredictiveScalingPolicy {
	now := metav1.NewTime(time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC))
	return &PredictiveScalingPolicy{
		Spec: PredictiveScalingPolicySpec{
			TargetRef: TargetReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "checkout-api",
			},
			Mode:                PredictiveScalingModeRecommendationOnly,
			ForecastHorizon:     metav1.Duration{Duration: 5 * time.Minute},
			Warmup:              WarmupSettings{EstimatedReadyDuration: metav1.Duration{Duration: 45 * time.Second}},
			ConfidenceThreshold: 0.7,
			MinReplicas:         2,
			MaxReplicas:         10,
			ScaleUp:             &ScaleStepPolicy{MaxReplicasChange: 4},
			ScaleDown:           &ScaleStepPolicy{MaxReplicasChange: 2},
			CooldownWindow:      metav1.Duration{Duration: 5 * time.Minute},
			BlackoutWindows: []BlackoutWindow{
				{
					Name:   "release-freeze",
					Start:  now,
					End:    metav1.NewTime(now.Add(30 * time.Minute)),
					Reason: "suppress recommendations during deploy freeze",
				},
			},
			KnownEvents: []KnownEvent{
				{
					Name:  "weekday-peak",
					Start: now,
					End:   metav1.NewTime(now.Add(15 * time.Minute)),
					Note:  "operator-identified traffic ramp",
				},
			},
			DependencyHealthChecks: []DependencyHealthCheck{
				{
					Name:            "payments",
					Type:            DependencyHealthCheckTypePrometheusQuery,
					Query:           "avg_over_time(payments_ready_ratio[5m])",
					MinHealthyRatio: 0.95,
				},
			},
			NodeHeadroomSanity: NodeHeadroomSanityRequireForScaleUp,
		},
	}
}
