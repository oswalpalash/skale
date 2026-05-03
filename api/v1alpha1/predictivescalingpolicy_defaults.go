package v1alpha1

import "time"

const (
	defaultForecastHorizon     = 5 * time.Minute
	defaultForecastContext     = 168 * time.Hour
	defaultForecastContextStep = 5 * time.Minute
	defaultRecentContext       = 2 * time.Hour
	defaultRecentContextStep   = 30 * time.Second
	defaultWarmupDuration      = 45 * time.Second
	defaultCooldownWindow      = 5 * time.Minute
	defaultConfidenceThreshold = 0.7
	defaultTargetUtilization   = 0.8
)

// Default applies conservative v1 defaults for recommendation-only operation.
func (p *PredictiveScalingPolicy) Default() {
	p.Spec.Default()
}

// Default applies defaults that keep the v1 policy surface explicit and bounded.
func (s *PredictiveScalingPolicySpec) Default() {
	if s.TargetRef.APIVersion == "" {
		s.TargetRef.APIVersion = "apps/v1"
	}
	if s.TargetRef.Kind == "" {
		s.TargetRef.Kind = "Deployment"
	}
	if s.Mode == "" {
		s.Mode = PredictiveScalingModeRecommendationOnly
	}
	if s.ForecastHorizon.Duration == 0 {
		s.ForecastHorizon.Duration = defaultForecastHorizon
	}
	if s.ForecastContextWindow.Duration == 0 {
		s.ForecastContextWindow.Duration = defaultForecastContext
	}
	if s.ForecastContextStep.Duration == 0 {
		s.ForecastContextStep.Duration = defaultForecastContextStep
	}
	if s.RecentContextWindow.Duration == 0 {
		s.RecentContextWindow.Duration = defaultRecentContext
	}
	if s.RecentContextStep.Duration == 0 {
		s.RecentContextStep.Duration = defaultRecentContextStep
	}
	if s.Warmup.EstimatedReadyDuration.Duration == 0 {
		s.Warmup.EstimatedReadyDuration.Duration = defaultWarmupDuration
	}
	if s.TargetUtilization == 0 {
		s.TargetUtilization = defaultTargetUtilization
	}
	if s.ConfidenceThreshold == 0 {
		s.ConfidenceThreshold = defaultConfidenceThreshold
	}
	if s.CooldownWindow.Duration == 0 {
		s.CooldownWindow.Duration = defaultCooldownWindow
	}
	if s.NodeHeadroomSanity == "" {
		s.NodeHeadroomSanity = NodeHeadroomSanityRequireForScaleUp
	}
	for i := range s.DependencyHealthChecks {
		if s.DependencyHealthChecks[i].Type == "" {
			s.DependencyHealthChecks[i].Type = DependencyHealthCheckTypePrometheusQuery
		}
	}
}
