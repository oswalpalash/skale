package benchmark

import (
	"math"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/replay"
	"github.com/oswalpalash/skale/internal/safety"
)

const (
	ScenarioDiurnalBurst          = "diurnal_burst"
	ScenarioScheduledBurst        = "scheduled_burst"
	ScenarioNoisyRecurringBurst   = "noisy_recurring_burst"
	ScenarioAnomalyOneOffSpike    = "anomaly_one_off_spike"
	ScenarioInsufficientTelemetry = "insufficient_telemetry"
	ScenarioHeadroomConstrained   = "node_headroom_constrained"
)

// Scenario is one repeatable synthetic workload case for replay and recommendation evaluation.
type Scenario struct {
	Name        string
	Description string
	Caveats     []string
	Fixture     Fixture
}

// Fixture is the synthetic replay input for one workload scenario.
//
// The fixture remains intentionally narrow: one replay spec, one normalized historical snapshot,
// and an optional time-indexed node headroom timeline used by the harness to inject request-based
// schedulability checks during recommendation evaluation.
type Fixture struct {
	Spec              replay.Spec
	Snapshot          metrics.Snapshot
	NodeHeadroom      []HeadroomObservation
	EffectivePerPod   float64
	FullPodCapacity   float64
	TargetUtilization float64
}

// HeadroomObservation is one synthetic request-based node headroom snapshot.
type HeadroomObservation struct {
	ObservedAt time.Time
	Signal     safety.NodeHeadroomSignal
}

// DefaultSuite returns the supported synthetic benchmark scenarios for the v1 wedge and failure modes.
func DefaultSuite() []Scenario {
	return []Scenario{
		DiurnalBurstScenario(),
		ScheduledBurstScenario(),
		NoisyRecurringBurstScenario(),
		AnomalyOneOffSpikeScenario(),
		InsufficientTelemetryScenario(),
		NodeHeadroomConstrainedScenario(),
	}
}

// DiurnalBurstScenario models a predictable daily burst with warmup lag and a reactive baseline.
func DiurnalBurstScenario() Scenario {
	const (
		pointsPerDay            = 96
		days                    = 4
		effectivePerPod         = 100
		targetUtilization       = 0.8
		fullPodCapacity         = effectivePerPod / targetUtilization
		minReplicas       int32 = 2
		maxReplicas       int32 = 8
	)

	start := time.Date(2026, time.March, 30, 0, 0, 0, 0, time.UTC)
	step := 15 * time.Minute
	demandDay := flatValues(pointsPerDay, 140)
	addRaisedWindow(demandDay, 24, 32, 40)
	addRaisedWindow(demandDay, 38, 44, 120)
	addRaisedWindow(demandDay, 44, 56, 220)
	addRaisedWindow(demandDay, 56, 64, 90)
	demand := repeatValues(demandDay, days)
	demand = addDeterministicNoise(demand, 8)
	replicas := reactiveReplicas(demand, effectivePerPod, minReplicas, maxReplicas, 2, 1, 1)

	fixture := newFixture(
		start,
		step,
		demand,
		replicas,
		newSpec(specConfig{
			namespace:           "payments",
			name:                "checkout-api",
			start:               start.Add(72 * time.Hour),
			end:                 start.Add(96*time.Hour - step),
			step:                step,
			lookback:            49 * time.Hour,
			workload:            "payments/checkout-api",
			forecastHorizon:     30 * time.Minute,
			forecastSeasonality: 24 * time.Hour,
			warmup:              30 * time.Minute,
			targetUtilization:   targetUtilization,
			confidenceThreshold: 0.65,
			minReplicas:         minReplicas,
			maxReplicas:         maxReplicas,
			maxStepUp:           int32Ptr(2),
			maxStepDown:         int32Ptr(1),
			cooldown:            30 * time.Minute,
			readinessLookback:   49 * time.Hour,
		}),
		effectivePerPod,
		fullPodCapacity,
		targetUtilization,
	)

	return Scenario{
		Name:        ScenarioDiurnalBurst,
		Description: "Predictable daily burst with enough history for replay to pre-scale ahead of a repeating peak.",
		Caveats: []string{
			"Baseline replicas are a lagged reactive proxy, not a reconstructed HPA algorithm.",
		},
		Fixture: fixture,
	}
}

// ScheduledBurstScenario models a fixed recurring burst window that should be easy to anticipate.
func ScheduledBurstScenario() Scenario {
	const (
		effectivePerPod         = 100
		targetUtilization       = 0.8
		fullPodCapacity         = effectivePerPod / targetUtilization
		minReplicas       int32 = 2
		maxReplicas       int32 = 8
	)

	start := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	step := 5 * time.Minute
	pattern := flatValues(24, 150)
	addRaisedWindow(pattern, 11, 12, 20)
	addRaisedWindow(pattern, 12, 16, 220)
	addRaisedWindow(pattern, 16, 17, 25)
	demand := repeatValues(pattern, 8)
	replicas := reactiveReplicas(demand, effectivePerPod, minReplicas, maxReplicas, 3, 1, 1)

	fixture := newFixture(
		start,
		step,
		demand,
		replicas,
		newSpec(specConfig{
			namespace:           "payments",
			name:                "cart-api",
			start:               start.Add(12 * time.Hour),
			end:                 start.Add(16*time.Hour - step),
			step:                step,
			lookback:            6 * time.Hour,
			workload:            "payments/cart-api",
			forecastHorizon:     20 * time.Minute,
			forecastSeasonality: 2 * time.Hour,
			warmup:              15 * time.Minute,
			targetUtilization:   targetUtilization,
			confidenceThreshold: 0.65,
			minReplicas:         minReplicas,
			maxReplicas:         maxReplicas,
			maxStepUp:           int32Ptr(2),
			maxStepDown:         int32Ptr(1),
			cooldown:            15 * time.Minute,
			readinessLookback:   6 * time.Hour,
		}),
		effectivePerPod,
		fullPodCapacity,
		targetUtilization,
	)

	return Scenario{
		Name:        ScenarioScheduledBurst,
		Description: "Recurring fixed-time burst that should yield clear predictive recommendation events.",
		Fixture:     fixture,
	}
}

// NoisyRecurringBurstScenario models a recurring burst with deterministic noise and less stable confidence.
func NoisyRecurringBurstScenario() Scenario {
	scenario := ScheduledBurstScenario()
	scenario.Name = ScenarioNoisyRecurringBurst
	scenario.Description = "Recurring burst with deterministic noise added to demand so replay still helps but confidence is less clean."

	demand := extractValues(scenario.Fixture.Snapshot.Demand)
	demand = addDeterministicNoise(demand, 22)
	replicas := reactiveReplicas(demand, scenario.Fixture.EffectivePerPod, 2, 8, 3, 1, 1)
	scenario.Fixture = newFixture(
		scenario.Fixture.Snapshot.Window.Start,
		5*time.Minute,
		demand,
		replicas,
		scenario.Fixture.Spec,
		scenario.Fixture.EffectivePerPod,
		scenario.Fixture.FullPodCapacity,
		scenario.Fixture.TargetUtilization,
	)
	return scenario
}

// AnomalyOneOffSpikeScenario models a short spike that should not look predictably forecastable.
func AnomalyOneOffSpikeScenario() Scenario {
	const (
		effectivePerPod         = 100
		targetUtilization       = 0.8
		fullPodCapacity         = effectivePerPod / targetUtilization
		minReplicas       int32 = 2
		maxReplicas       int32 = 8
	)

	start := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	step := 5 * time.Minute
	points := 96
	demand := flatValues(points, 150)
	addRaisedWindow(demand, 58, 60, 360)
	demand = addDeterministicNoise(demand, 6)
	replicas := reactiveReplicas(demand, effectivePerPod, minReplicas, maxReplicas, 2, 1, 1)

	fixture := newFixture(
		start,
		step,
		demand,
		replicas,
		newSpec(specConfig{
			namespace:           "payments",
			name:                "risk-api",
			start:               start.Add(4 * time.Hour),
			end:                 start.Add(8*time.Hour - step),
			step:                step,
			lookback:            4 * time.Hour,
			workload:            "payments/risk-api",
			forecastHorizon:     15 * time.Minute,
			forecastSeasonality: 0,
			warmup:              15 * time.Minute,
			targetUtilization:   targetUtilization,
			confidenceThreshold: 0.65,
			minReplicas:         minReplicas,
			maxReplicas:         maxReplicas,
			maxStepUp:           int32Ptr(2),
			maxStepDown:         int32Ptr(1),
			cooldown:            15 * time.Minute,
			readinessLookback:   4 * time.Hour,
		}),
		effectivePerPod,
		fullPodCapacity,
		targetUtilization,
	)

	return Scenario{
		Name:        ScenarioAnomalyOneOffSpike,
		Description: "Short one-off spike where warmup is longer than the burst, so replay should not pretend predictive scaling solved it.",
		Caveats: []string{
			"Replay still sees the post-spike demand history; any late recommendation is expected to arrive after the spike has already passed.",
		},
		Fixture: fixture,
	}
}

// InsufficientTelemetryScenario models a workload whose telemetry should fail readiness checks.
func InsufficientTelemetryScenario() Scenario {
	scenario := ScheduledBurstScenario()
	scenario.Name = ScenarioInsufficientTelemetry
	scenario.Description = "Required telemetry is missing, so replay should fail closed as unsupported."
	scenario.Fixture.Snapshot.CPU = nil
	scenario.Fixture.Spec.Target.Name = "profile-api"
	scenario.Fixture.Spec.Policy.Workload = "payments/profile-api"
	return scenario
}

// NodeHeadroomConstrainedScenario models a predictable burst that is suppressed by insufficient request-based headroom.
func NodeHeadroomConstrainedScenario() Scenario {
	scenario := ScheduledBurstScenario()
	scenario.Name = ScenarioHeadroomConstrained
	scenario.Description = "Predictable burst where replay would scale up, but request-based node headroom makes that recommendation plausibly unschedulable."
	scenario.Fixture.Spec.Target.Name = "ledger-api"
	scenario.Fixture.Spec.Policy.Workload = "payments/ledger-api"
	scenario.Fixture.Spec.Policy.NodeHeadroomMode = safety.NodeHeadroomModeRequireForScaleUp
	scenario.Fixture.NodeHeadroom = []HeadroomObservation{{
		ObservedAt: scenario.Fixture.Snapshot.Window.Start,
		Signal: safety.NodeHeadroomSignal{
			State:      safety.NodeHeadroomStateReady,
			ObservedAt: scenario.Fixture.Snapshot.Window.Start,
			PodRequests: safety.Resources{
				CPUMilli:    750,
				MemoryBytes: 512 * 1024 * 1024,
			},
			ClusterSummary: safety.AllocatableSummary{
				Allocatable: safety.Resources{
					CPUMilli:    8000,
					MemoryBytes: 8 * 1024 * 1024 * 1024,
				},
				Requested: safety.Resources{
					CPUMilli:    6500,
					MemoryBytes: 7 * 1024 * 1024 * 1024,
				},
			},
			Nodes: []safety.NodeAllocatableSummary{
				{
					Name:        "worker-a",
					Schedulable: true,
					Summary: safety.AllocatableSummary{
						Allocatable: safety.Resources{
							CPUMilli:    4000,
							MemoryBytes: 4 * 1024 * 1024 * 1024,
						},
						Requested: safety.Resources{
							CPUMilli:    3500,
							MemoryBytes: 4026531840,
						},
					},
				},
				{
					Name:        "worker-b",
					Schedulable: true,
					Summary: safety.AllocatableSummary{
						Allocatable: safety.Resources{
							CPUMilli:    4000,
							MemoryBytes: 4 * 1024 * 1024 * 1024,
						},
						Requested: safety.Resources{
							CPUMilli:    3000,
							MemoryBytes: 4026531840,
						},
					},
				},
			},
		},
	}}
	return scenario
}

type specConfig struct {
	namespace           string
	name                string
	start               time.Time
	end                 time.Time
	step                time.Duration
	lookback            time.Duration
	workload            string
	forecastHorizon     time.Duration
	forecastSeasonality time.Duration
	warmup              time.Duration
	targetUtilization   float64
	confidenceThreshold float64
	minReplicas         int32
	maxReplicas         int32
	maxStepUp           *int32
	maxStepDown         *int32
	cooldown            time.Duration
	readinessLookback   time.Duration
}

func newSpec(cfg specConfig) replay.Spec {
	return replay.Spec{
		Target: metrics.Target{
			Namespace: cfg.namespace,
			Name:      cfg.name,
		},
		Window: metrics.Window{
			Start: cfg.start,
			End:   cfg.end,
		},
		Step:     cfg.step,
		Lookback: cfg.lookback,
		Policy: replay.Policy{
			Workload:            cfg.workload,
			ForecastHorizon:     cfg.forecastHorizon,
			ForecastSeasonality: cfg.forecastSeasonality,
			Warmup:              cfg.warmup,
			TargetUtilization:   cfg.targetUtilization,
			ConfidenceThreshold: cfg.confidenceThreshold,
			MinReplicas:         cfg.minReplicas,
			MaxReplicas:         cfg.maxReplicas,
			MaxStepUp:           cfg.maxStepUp,
			MaxStepDown:         cfg.maxStepDown,
			CooldownWindow:      cfg.cooldown,
		},
		Options: replay.Options{
			CapacityLookback:       cfg.lookback,
			MinimumCapacitySamples: 4,
			ReadinessOptions: metrics.ReadinessOptions{
				MinimumLookback:                   cfg.readinessLookback,
				ExpectedResolution:                cfg.step,
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
	}
}

func newFixture(
	start time.Time,
	step time.Duration,
	demand []float64,
	replicas []float64,
	spec replay.Spec,
	effectivePerPod float64,
	fullPodCapacity float64,
	targetUtilization float64,
) Fixture {
	snapshotStart := start
	snapshotEnd := start.Add(time.Duration(len(demand)-1) * step)
	cpu := cpuSaturationSeries(demand, replicas, fullPodCapacity)
	memory := memorySaturationSeries(cpu)
	warmup := constantSeries(len(demand), spec.Policy.Warmup.Seconds())

	return Fixture{
		Spec: spec,
		Snapshot: metrics.Snapshot{
			Window: metrics.Window{
				Start: snapshotStart,
				End:   snapshotEnd,
			},
			Demand:   buildSeries(metrics.SignalDemand, "rps", snapshotStart, step, demand),
			Replicas: buildSeries(metrics.SignalReplicas, "replicas", snapshotStart, step, replicas),
			CPU:      seriesPtr(buildSeries(metrics.SignalCPU, "ratio", snapshotStart, step, cpu)),
			Memory:   seriesPtr(buildSeries(metrics.SignalMemory, "ratio", snapshotStart, step, memory)),
			Warmup:   seriesPtr(buildSeries(metrics.SignalWarmup, "seconds", snapshotStart, step, warmup)),
		},
		EffectivePerPod:   effectivePerPod,
		FullPodCapacity:   fullPodCapacity,
		TargetUtilization: targetUtilization,
	}
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

func extractValues(series metrics.SignalSeries) []float64 {
	out := make([]float64, 0, len(series.Samples))
	for _, sample := range series.Samples {
		out = append(out, sample.Value)
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
		ObservedLabelSignatures: []string{"service=synthetic"},
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
