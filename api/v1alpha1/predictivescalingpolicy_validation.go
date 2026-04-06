package v1alpha1

import (
	"time"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

const maxForecastHorizon = 30 * time.Minute

// Validate returns schema-level validation errors that are easier to unit test before webhook wiring exists.
func (p *PredictiveScalingPolicy) Validate() field.ErrorList {
	return p.Spec.Validate(field.NewPath("spec"))
}

// Validate checks v1 policy constraints that are awkward or impossible to express with OpenAPI markers alone.
func (s *PredictiveScalingPolicySpec) Validate(path *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if s.TargetRef.Name == "" {
		allErrs = append(allErrs, field.Required(path.Child("targetRef", "name"), "target deployment name is required"))
	}
	if s.TargetRef.APIVersion != "" && s.TargetRef.APIVersion != "apps/v1" {
		allErrs = append(allErrs, field.NotSupported(path.Child("targetRef", "apiVersion"), s.TargetRef.APIVersion, []string{"apps/v1"}))
	}
	if s.TargetRef.Kind != "" && s.TargetRef.Kind != "Deployment" {
		allErrs = append(allErrs, field.NotSupported(path.Child("targetRef", "kind"), s.TargetRef.Kind, []string{"Deployment"}))
	}
	if s.Mode != "" && s.Mode != PredictiveScalingModeRecommendationOnly {
		allErrs = append(allErrs, field.NotSupported(path.Child("mode"), s.Mode, []string{string(PredictiveScalingModeRecommendationOnly)}))
	}
	if s.ForecastHorizon.Duration <= 0 {
		allErrs = append(allErrs, field.Invalid(path.Child("forecastHorizon"), s.ForecastHorizon.String(), "must be greater than zero"))
	}
	if s.ForecastHorizon.Duration > maxForecastHorizon {
		allErrs = append(allErrs, field.Invalid(path.Child("forecastHorizon"), s.ForecastHorizon.String(), "must be 30m or less in v1"))
	}
	if s.Warmup.EstimatedReadyDuration.Duration <= 0 {
		allErrs = append(allErrs, field.Invalid(path.Child("warmup", "estimatedReadyDuration"), s.Warmup.EstimatedReadyDuration.String(), "must be greater than zero"))
	}
	if s.ConfidenceThreshold < 0.1 || s.ConfidenceThreshold > 1 {
		allErrs = append(allErrs, field.Invalid(path.Child("confidenceThreshold"), s.ConfidenceThreshold, "must be between 0.1 and 1"))
	}
	if s.MinReplicas < 1 {
		allErrs = append(allErrs, field.Invalid(path.Child("minReplicas"), s.MinReplicas, "must be at least 1"))
	}
	if s.MaxReplicas < 1 {
		allErrs = append(allErrs, field.Invalid(path.Child("maxReplicas"), s.MaxReplicas, "must be at least 1"))
	}
	if s.MinReplicas > s.MaxReplicas {
		allErrs = append(allErrs, field.Invalid(path.Child("minReplicas"), s.MinReplicas, "must be less than or equal to maxReplicas"))
	}
	if s.ScaleUp != nil && s.ScaleUp.MaxReplicasChange < 1 {
		allErrs = append(allErrs, field.Invalid(path.Child("scaleUp", "maxReplicasChange"), s.ScaleUp.MaxReplicasChange, "must be at least 1"))
	}
	if s.ScaleDown != nil && s.ScaleDown.MaxReplicasChange < 1 {
		allErrs = append(allErrs, field.Invalid(path.Child("scaleDown", "maxReplicasChange"), s.ScaleDown.MaxReplicasChange, "must be at least 1"))
	}
	if s.CooldownWindow.Duration < 0 {
		allErrs = append(allErrs, field.Invalid(path.Child("cooldownWindow"), s.CooldownWindow.String(), "must not be negative"))
	}

	for i, window := range s.BlackoutWindows {
		windowPath := path.Child("blackoutWindows").Index(i)
		if window.Name == "" {
			allErrs = append(allErrs, field.Required(windowPath.Child("name"), "name is required"))
		}
		if !window.Start.Before(&window.End) {
			allErrs = append(allErrs, field.Invalid(windowPath.Child("end"), window.End, "must be after start"))
		}
	}

	for i, event := range s.KnownEvents {
		eventPath := path.Child("knownEvents").Index(i)
		if event.Name == "" {
			allErrs = append(allErrs, field.Required(eventPath.Child("name"), "name is required"))
		}
		if !event.Start.Before(&event.End) {
			allErrs = append(allErrs, field.Invalid(eventPath.Child("end"), event.End, "must be after start"))
		}
	}

	for i, check := range s.DependencyHealthChecks {
		checkPath := path.Child("dependencyHealthChecks").Index(i)
		if check.Name == "" {
			allErrs = append(allErrs, field.Required(checkPath.Child("name"), "name is required"))
		}
		if check.Type != "" && check.Type != DependencyHealthCheckTypePrometheusQuery {
			allErrs = append(allErrs, field.NotSupported(checkPath.Child("type"), check.Type, []string{string(DependencyHealthCheckTypePrometheusQuery)}))
		}
		if check.Query == "" {
			allErrs = append(allErrs, field.Required(checkPath.Child("query"), "query is required"))
		}
		if check.MinHealthyRatio < 0.01 || check.MinHealthyRatio > 1 {
			allErrs = append(allErrs, field.Invalid(checkPath.Child("minHealthyRatio"), check.MinHealthyRatio, "must be between 0.01 and 1"))
		}
	}

	if s.NodeHeadroomSanity != "" &&
		s.NodeHeadroomSanity != NodeHeadroomSanityDisabled &&
		s.NodeHeadroomSanity != NodeHeadroomSanityRequireForScaleUp {
		allErrs = append(allErrs, field.NotSupported(path.Child("nodeHeadroomSanity"), s.NodeHeadroomSanity, []string{string(NodeHeadroomSanityDisabled), string(NodeHeadroomSanityRequireForScaleUp)}))
	}

	return allErrs
}
