package livefixture

import (
	"fmt"
	"slices"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/replayinput"
	"github.com/oswalpalash/skale/internal/safety"
)

const observedLabelSignature = "live-hpa-demo"

// Sample is one captured live-demo observation used to construct a replay input.
type Sample struct {
	Timestamp     time.Time
	DemandQPS     float64
	ReadyReplicas int32
	CPURatio      float64
	MemoryRatio   float64
}

// Config defines the explicit replay policy and capture framing for a live replay input.
type Config struct {
	Target               metrics.Target
	Workload             string
	SchemaVersion        string
	Step                 time.Duration
	ReplayDuration       time.Duration
	Lookback             time.Duration
	ForecastHorizon      time.Duration
	ForecastSeasonality  time.Duration
	Warmup               time.Duration
	TargetUtilization    float64
	ConfidenceThreshold  float64
	MinReplicas          int32
	MaxReplicas          int32
	MaxStepUp            *int32
	MaxStepDown          *int32
	CooldownWindow       time.Duration
	NodeHeadroomMode     safety.NodeHeadroomMode
	CapacityLookback     time.Duration
	MinimumCapacitySteps int
}

// BuildDocument converts captured live samples into the narrow replay-input document used by replayctl
// and the controller's recommendation-only demo mode.
//
// This stays intentionally explicit. The resulting replay input still reflects assumptions:
// - demand is the captured injected request rate for the demo run
// - replicas are the observed ready replicas from the target Deployment
// - warmup is a configured fixed delay, not inferred from scheduler internals
func BuildDocument(config Config, samples []Sample) (replayinput.Document, error) {
	if len(samples) == 0 {
		return replayinput.Document{}, fmt.Errorf("live fixture requires at least one captured sample")
	}
	if config.Target.Name == "" || config.Target.Namespace == "" {
		return replayinput.Document{}, fmt.Errorf("live fixture target namespace/name are required")
	}
	if config.Step <= 0 {
		return replayinput.Document{}, fmt.Errorf("live fixture step must be positive")
	}
	if config.ReplayDuration <= 0 {
		return replayinput.Document{}, fmt.Errorf("live fixture replay duration must be positive")
	}
	if config.Lookback <= 0 {
		return replayinput.Document{}, fmt.Errorf("live fixture lookback must be positive")
	}

	sorted := slices.Clone(samples)
	slices.SortFunc(sorted, func(left, right Sample) int {
		switch {
		case left.Timestamp.Before(right.Timestamp):
			return -1
		case left.Timestamp.After(right.Timestamp):
			return 1
		default:
			return 0
		}
	})

	for index := range sorted {
		if index == 0 {
			continue
		}
		if !sorted[index].Timestamp.After(sorted[index-1].Timestamp) {
			return replayinput.Document{}, fmt.Errorf("live fixture samples must be strictly increasing")
		}
	}

	snapshotStart := sorted[0].Timestamp.UTC()
	snapshotEnd := sorted[len(sorted)-1].Timestamp.UTC()
	replayEnd := snapshotEnd
	replayStart := replayEnd.Add(-config.ReplayDuration)
	if replayStart.Before(snapshotStart) {
		return replayinput.Document{}, fmt.Errorf(
			"live fixture replay duration %s exceeds captured sample history %s",
			config.ReplayDuration,
			snapshotEnd.Sub(snapshotStart),
		)
	}

	if config.SchemaVersion == "" {
		config.SchemaVersion = "v1alpha1"
	}
	if config.CapacityLookback <= 0 {
		config.CapacityLookback = config.Lookback
	}
	if config.MinimumCapacitySteps <= 0 {
		config.MinimumCapacitySteps = 4
	}

	demand := make([]float64, 0, len(sorted))
	replicas := make([]float64, 0, len(sorted))
	cpu := make([]float64, 0, len(sorted))
	memory := make([]float64, 0, len(sorted))
	warmup := make([]float64, 0, len(sorted))
	for _, sample := range sorted {
		demand = append(demand, sample.DemandQPS)
		replicas = append(replicas, float64(sample.ReadyReplicas))
		cpu = append(cpu, sample.CPURatio)
		memory = append(memory, sample.MemoryRatio)
		warmup = append(warmup, config.Warmup.Seconds())
	}

	return replayinput.Document{
		SchemaVersion: config.SchemaVersion,
		Target:        config.Target,
		Window: replayinput.WindowDocument{
			Start: replayStart,
			End:   replayEnd,
		},
		Step:     duration(config.Step),
		Lookback: duration(config.Lookback),
		Policy: replayinput.PolicyDocument{
			Workload:            config.Workload,
			ForecastHorizon:     duration(config.ForecastHorizon),
			ForecastSeasonality: duration(config.ForecastSeasonality),
			Warmup:              duration(config.Warmup),
			TargetUtilization:   config.TargetUtilization,
			ConfidenceThreshold: config.ConfidenceThreshold,
			MinReplicas:         config.MinReplicas,
			MaxReplicas:         config.MaxReplicas,
			MaxStepUp:           config.MaxStepUp,
			MaxStepDown:         config.MaxStepDown,
			CooldownWindow:      duration(config.CooldownWindow),
			NodeHeadroomMode:    config.NodeHeadroomMode,
		},
		Options: replayinput.OptionsDocument{
			CapacityLookback:       duration(config.CapacityLookback),
			MinimumCapacitySamples: config.MinimumCapacitySteps,
			Readiness: replayinput.ReadinessOptionsDocument{
				MinimumLookback:                   duration(config.Lookback),
				ExpectedResolution:                duration(config.Step),
				DegradedMissingFraction:           0.10,
				UnsupportedMissingFraction:        0.25,
				DegradedResolutionMultiplier:      2,
				UnsupportedResolutionMultiplier:   4,
				DegradedGapMultiplier:             2,
				UnsupportedGapMultiplier:          4,
				MinimumWarmupSamplesToEstimate:    3,
				DemandStepChangeThreshold:         2.0,
				DegradedDemandUnstableFraction:    0.8,
				UnsupportedDemandUnstableFraction: 1.0,
			},
		},
		Snapshot: metrics.Snapshot{
			Window: metrics.Window{
				Start: snapshotStart,
				End:   snapshotEnd,
			},
			Demand:   buildSeries(metrics.SignalDemand, "rps", sorted, demand),
			Replicas: buildSeries(metrics.SignalReplicas, "replicas", sorted, replicas),
			CPU:      seriesPtr(buildSeries(metrics.SignalCPU, "ratio", sorted, cpu)),
			Memory:   seriesPtr(buildSeries(metrics.SignalMemory, "ratio", sorted, memory)),
			Warmup:   seriesPtr(buildSeries(metrics.SignalWarmup, "seconds", sorted, warmup)),
		},
	}, nil
}

func duration(value time.Duration) replayinput.DurationValue {
	return replayinput.DurationValue{Duration: value}
}

func buildSeries(name metrics.SignalName, unit string, samples []Sample, values []float64) metrics.SignalSeries {
	points := make([]metrics.Sample, 0, len(samples))
	for index, sample := range samples {
		points = append(points, metrics.Sample{
			Timestamp: sample.Timestamp.UTC(),
			Value:     values[index],
		})
	}
	return metrics.SignalSeries{
		Name:                    name,
		Unit:                    unit,
		ObservedLabelSignatures: []string{observedLabelSignature},
		Samples:                 points,
	}
}

func seriesPtr(series metrics.SignalSeries) *metrics.SignalSeries {
	return &series
}
