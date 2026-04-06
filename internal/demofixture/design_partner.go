package demofixture

import (
	"math"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/replayinput"
	"github.com/oswalpalash/skale/internal/safety"
)

const (
	designPartnerScenarioName = "design-partner-24h"
)

// DesignPartner24HourDocument returns the richer design-partner replay input used by the
// local recommendation-only demo path.
//
// The generated fixture is intentionally synthetic:
// - it models a recurring daily burst pattern that fits the v1 wedge
// - the "actual" replica line is a lagged HPA-style baseline, not real cluster history
// - the cluster demo still uses a real Deployment, HPA object, and PredictiveScalingPolicy
//
// The goal is a repeatable 24-hour replay artifact that remains honest about what is real
// and what is simulated.
func DesignPartner24HourDocument() replayinput.Document {
	const (
		pointsPerDay            = 96
		days                    = 4
		effectivePerPod         = 100
		targetUtilization       = 0.8
		fullPodCapacity         = effectivePerPod / targetUtilization
		minReplicas       int32 = 2
		maxReplicas       int32 = 4
	)

	seriesStart := time.Date(2026, time.March, 30, 0, 0, 0, 0, time.UTC)
	windowStart := time.Date(2026, time.April, 1, 11, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, time.April, 2, 10, 45, 0, 0, time.UTC)
	step := 15 * time.Minute

	demandDay := flatValues(pointsPerDay, 140)
	addRaisedWindow(demandDay, 24, 32, 40)
	addRaisedWindow(demandDay, 38, 44, 120)
	addRaisedWindow(demandDay, 44, 56, 220)
	addRaisedWindow(demandDay, 56, 64, 90)
	addRaisedWindow(demandDay, 72, 78, 130)

	demand := repeatValues(demandDay, days)
	demand = addDeterministicNoise(demand, 8)
	replicas := reactiveReplicas(demand, effectivePerPod, minReplicas, maxReplicas, 2, 2, 1)
	cpu := cpuSaturationSeries(demand, replicas, fullPodCapacity)
	memory := memorySaturationSeries(cpu)
	warmup := constantSeries(len(demand), 30*time.Minute.Seconds())

	sampleCount := samplesUntil(seriesStart, windowEnd, step)
	demand = demand[:sampleCount]
	replicas = replicas[:sampleCount]
	cpu = cpu[:sampleCount]
	memory = memory[:sampleCount]
	warmup = warmup[:sampleCount]

	return replayinput.Document{
		SchemaVersion: "v1alpha1",
		Target: metrics.Target{
			Namespace: "skale-demo",
			Name:      "checkout-api",
		},
		Window: replayinput.WindowDocument{
			Start: windowStart,
			End:   windowEnd,
		},
		Step:     duration(step),
		Lookback: duration(49 * time.Hour),
		Policy: replayinput.PolicyDocument{
			Workload:            "skale-demo/checkout-api",
			ForecastHorizon:     duration(30 * time.Minute),
			ForecastSeasonality: duration(24 * time.Hour),
			Warmup:              duration(30 * time.Minute),
			TargetUtilization:   targetUtilization,
			ConfidenceThreshold: 0.65,
			MinReplicas:         minReplicas,
			MaxReplicas:         maxReplicas,
			MaxStepUp:           int32Ptr(2),
			MaxStepDown:         int32Ptr(1),
			CooldownWindow:      duration(30 * time.Minute),
			NodeHeadroomMode:    safety.NodeHeadroomModeDisabled,
		},
		Options: replayinput.OptionsDocument{
			CapacityLookback:       duration(49 * time.Hour),
			MinimumCapacitySamples: 4,
			Readiness: replayinput.ReadinessOptionsDocument{
				MinimumLookback:                   duration(49 * time.Hour),
				ExpectedResolution:                duration(step),
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
				Start: seriesStart,
				End:   windowEnd,
			},
			Demand:   buildSeries(metrics.SignalDemand, "rps", seriesStart, step, demand),
			Replicas: buildSeries(metrics.SignalReplicas, "replicas", seriesStart, step, replicas),
			CPU:      seriesPtr(buildSeries(metrics.SignalCPU, "ratio", seriesStart, step, cpu)),
			Memory:   seriesPtr(buildSeries(metrics.SignalMemory, "ratio", seriesStart, step, memory)),
			Warmup:   seriesPtr(buildSeries(metrics.SignalWarmup, "seconds", seriesStart, step, warmup)),
		},
	}
}

func duration(value time.Duration) replayinput.DurationValue {
	return replayinput.DurationValue{Duration: value}
}

func samplesUntil(start, end time.Time, step time.Duration) int {
	if step <= 0 || end.Before(start) {
		return 0
	}
	return int(end.Sub(start)/step) + 1
}

func reactiveReplicas(demand []float64, effectivePerPod float64, minReplicas, maxReplicas int32, lagSteps int, maxStepUp, maxStepDown int32) []float64 {
	out := make([]float64, 0, len(demand))
	current := minReplicas
	for index := range demand {
		source := index - lagSteps
		if source < 0 {
			source = 0
		}
		desired := int32(math.Ceil(demand[source] / effectivePerPod))
		if desired < minReplicas {
			desired = minReplicas
		}
		if desired > maxReplicas {
			desired = maxReplicas
		}
		if delta := desired - current; delta > maxStepUp {
			desired = current + maxStepUp
		}
		if delta := current - desired; delta > maxStepDown {
			desired = current - maxStepDown
		}
		current = desired
		out = append(out, float64(current))
	}
	return out
}

func cpuSaturationSeries(demand, replicas []float64, fullPodCapacity float64) []float64 {
	out := make([]float64, 0, len(demand))
	for index := range demand {
		value := 0.0
		if replicas[index] > 0 && fullPodCapacity > 0 {
			value = demand[index] / (replicas[index] * fullPodCapacity)
		}
		out = append(out, clamp(value, 0.10, 1.40))
	}
	return out
}

func memorySaturationSeries(cpu []float64) []float64 {
	out := make([]float64, 0, len(cpu))
	for _, value := range cpu {
		out = append(out, clamp(0.42+value*0.32, 0.25, 0.92))
	}
	return out
}

func constantSeries(count int, value float64) []float64 {
	out := make([]float64, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, value)
	}
	return out
}

func flatValues(count int, value float64) []float64 {
	return constantSeries(count, value)
}

func repeatValues(pattern []float64, repeats int) []float64 {
	out := make([]float64, 0, len(pattern)*repeats)
	for i := 0; i < repeats; i++ {
		out = append(out, pattern...)
	}
	return out
}

func addRaisedWindow(values []float64, start, end int, increase float64) {
	for index := range values {
		if index < start || index >= end {
			continue
		}
		window := float64(end - start)
		offset := float64(index-start) + 0.5
		multiplier := math.Sin(math.Pi * offset / window)
		if multiplier < 0 {
			multiplier = 0
		}
		values[index] += increase * multiplier
	}
}

func addDeterministicNoise(values []float64, amplitude float64) []float64 {
	out := make([]float64, 0, len(values))
	for index, value := range values {
		noise := amplitude * (0.65*math.Sin(float64(index)*0.73) + 0.35*math.Sin(float64(index)*1.91+0.8))
		out = append(out, math.Max(0, value+noise))
	}
	return out
}

func buildSeries(name metrics.SignalName, unit string, start time.Time, step time.Duration, values []float64) metrics.SignalSeries {
	samples := make([]metrics.Sample, 0, len(values))
	for index, value := range values {
		samples = append(samples, metrics.Sample{
			Timestamp: start.Add(time.Duration(index) * step),
			Value:     value,
		})
	}
	return metrics.SignalSeries{
		Name:                    name,
		Unit:                    unit,
		ObservedLabelSignatures: []string{designPartnerScenarioName},
		Samples:                 samples,
	}
}

func seriesPtr(series metrics.SignalSeries) *metrics.SignalSeries {
	return &series
}

func int32Ptr(value int32) *int32 {
	return &value
}

func clamp(value, lower, upper float64) float64 {
	if value < lower {
		return lower
	}
	if value > upper {
		return upper
	}
	return value
}
