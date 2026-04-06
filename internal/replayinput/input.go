package replayinput

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/replay"
	"github.com/oswalpalash/skale/internal/safety"
)

// Document is the narrow offline replay contract used by replayctl and the
// controller's demo-only static metrics mode.
//
// The file contains both the replay spec and the normalized historical snapshot
// so operators can inspect the exact evidence being replayed or surfaced in demo mode.
type Document struct {
	SchemaVersion string           `json:"schemaVersion,omitempty"`
	Target        metrics.Target   `json:"target"`
	Window        WindowDocument   `json:"window"`
	Step          DurationValue    `json:"step,omitempty"`
	Lookback      DurationValue    `json:"lookback,omitempty"`
	Policy        PolicyDocument   `json:"policy"`
	Options       OptionsDocument  `json:"options,omitempty"`
	Snapshot      metrics.Snapshot `json:"snapshot"`
}

type WindowDocument struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type PolicyDocument struct {
	Workload            string                  `json:"workload,omitempty"`
	ForecastHorizon     DurationValue           `json:"forecastHorizon,omitempty"`
	ForecastSeasonality DurationValue           `json:"forecastSeasonality,omitempty"`
	Warmup              DurationValue           `json:"warmup,omitempty"`
	TargetUtilization   float64                 `json:"targetUtilization,omitempty"`
	ConfidenceThreshold float64                 `json:"confidenceThreshold,omitempty"`
	MinReplicas         int32                   `json:"minReplicas,omitempty"`
	MaxReplicas         int32                   `json:"maxReplicas,omitempty"`
	MaxStepUp           *int32                  `json:"maxStepUp,omitempty"`
	MaxStepDown         *int32                  `json:"maxStepDown,omitempty"`
	CooldownWindow      DurationValue           `json:"cooldownWindow,omitempty"`
	BlackoutWindows     []safety.BlackoutWindow `json:"blackoutWindows,omitempty"`
	KnownEvents         []KnownEventDocument    `json:"knownEvents,omitempty"`
	DependencyHealth    []DependencyDocument    `json:"dependencyHealth,omitempty"`
	NodeHeadroomMode    safety.NodeHeadroomMode `json:"nodeHeadroomMode,omitempty"`
}

type OptionsDocument struct {
	BaselineMode           replay.BaselineMode      `json:"baselineMode,omitempty"`
	CapacityLookback       DurationValue            `json:"capacityLookback,omitempty"`
	MinimumCapacitySamples int                      `json:"minimumCapacitySamples,omitempty"`
	Readiness              ReadinessOptionsDocument `json:"readiness,omitempty"`
	HeadroomTimeline       []HeadroomObservation    `json:"headroomTimeline,omitempty"`
}

type KnownEventDocument struct {
	Name  string    `json:"name,omitempty"`
	Start time.Time `json:"start,omitempty"`
	End   time.Time `json:"end,omitempty"`
	Note  string    `json:"note,omitempty"`
}

type DependencyDocument struct {
	Name                string  `json:"name,omitempty"`
	Healthy             bool    `json:"healthy,omitempty"`
	HealthyRatio        float64 `json:"healthyRatio,omitempty"`
	MinimumHealthyRatio float64 `json:"minimumHealthyRatio,omitempty"`
	Message             string  `json:"message,omitempty"`
}

type HeadroomObservation struct {
	ObservedAt time.Time                 `json:"observedAt,omitempty"`
	Signal     safety.NodeHeadroomSignal `json:"signal,omitempty"`
}

type ReadinessOptionsDocument struct {
	MinimumLookback                   DurationValue `json:"minimumLookback,omitempty"`
	ExpectedResolution                DurationValue `json:"expectedResolution,omitempty"`
	DegradedMissingFraction           float64       `json:"degradedMissingFraction,omitempty"`
	UnsupportedMissingFraction        float64       `json:"unsupportedMissingFraction,omitempty"`
	DegradedResolutionMultiplier      float64       `json:"degradedResolutionMultiplier,omitempty"`
	UnsupportedResolutionMultiplier   float64       `json:"unsupportedResolutionMultiplier,omitempty"`
	DegradedGapMultiplier             float64       `json:"degradedGapMultiplier,omitempty"`
	UnsupportedGapMultiplier          float64       `json:"unsupportedGapMultiplier,omitempty"`
	MinimumWarmupSamplesToEstimate    int           `json:"minimumWarmupSamplesToEstimate,omitempty"`
	DemandStepChangeThreshold         float64       `json:"demandStepChangeThreshold,omitempty"`
	DegradedDemandUnstableFraction    float64       `json:"degradedDemandUnstableFraction,omitempty"`
	UnsupportedDemandUnstableFraction float64       `json:"unsupportedDemandUnstableFraction,omitempty"`
}

type DurationValue struct {
	time.Duration
}

func (d DurationValue) IsZero() bool {
	return d.Duration == 0
}

func (d DurationValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}

func (d *DurationValue) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch string(data) {
	case "", "null":
		d.Duration = 0
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("duration must be a JSON string such as \"5m\": %w", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		d.Duration = 0
		return nil
	}

	parsed, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", text, err)
	}
	d.Duration = parsed
	return nil
}

// StaticProvider is a narrow fixture-backed metrics provider used by replayctl
// and the controller's demo-only mode.
type StaticProvider struct {
	Target   metrics.Target
	Snapshot metrics.Snapshot
}

func (p StaticProvider) LoadWindow(_ context.Context, target metrics.Target, _ metrics.Window) (metrics.Snapshot, error) {
	if p.Target.Name != "" && (target.Name != p.Target.Name || target.Namespace != p.Target.Namespace) {
		return metrics.Snapshot{}, fmt.Errorf(
			"static replay input targets %s/%s, not %s/%s",
			p.Target.Namespace,
			p.Target.Name,
			target.Namespace,
			target.Name,
		)
	}
	return p.Snapshot, nil
}

// LoadFile reads one replay-input JSON document and returns the derived replay
// spec plus a matching static metrics provider.
func LoadFile(path string) (replay.Spec, metrics.Provider, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return replay.Spec{}, nil, fmt.Errorf("read replay input %q: %w", path, err)
	}

	var document Document
	if err := json.Unmarshal(bytes, &document); err != nil {
		return replay.Spec{}, nil, fmt.Errorf("decode replay input %q: %w", path, err)
	}

	spec, snapshot := document.ReplaySpecAndSnapshot()
	return spec, StaticProvider{Target: spec.Target, Snapshot: snapshot}, nil
}

func (d Document) ReplaySpecAndSnapshot() (replay.Spec, metrics.Snapshot) {
	spec := replay.Spec{
		Target: d.Target,
		Window: metrics.Window{
			Start: d.Window.Start,
			End:   d.Window.End,
		},
		Step:     d.Step.Duration,
		Lookback: d.Lookback.Duration,
		Policy: replay.Policy{
			Workload:            d.Policy.Workload,
			ForecastHorizon:     d.Policy.ForecastHorizon.Duration,
			ForecastSeasonality: d.Policy.ForecastSeasonality.Duration,
			Warmup:              d.Policy.Warmup.Duration,
			TargetUtilization:   d.Policy.TargetUtilization,
			ConfidenceThreshold: d.Policy.ConfidenceThreshold,
			MinReplicas:         d.Policy.MinReplicas,
			MaxReplicas:         d.Policy.MaxReplicas,
			MaxStepUp:           d.Policy.MaxStepUp,
			MaxStepDown:         d.Policy.MaxStepDown,
			CooldownWindow:      d.Policy.CooldownWindow.Duration,
			BlackoutWindows:     append([]safety.BlackoutWindow(nil), d.Policy.BlackoutWindows...),
			KnownEvents:         replayKnownEvents(d.Policy.KnownEvents),
			DependencyHealth:    dependencyStatuses(d.Policy.DependencyHealth),
			NodeHeadroomMode:    d.Policy.NodeHeadroomMode,
		},
		Options: replay.Options{
			BaselineMode:           d.Options.BaselineMode,
			CapacityLookback:       d.Options.CapacityLookback.Duration,
			MinimumCapacitySamples: d.Options.MinimumCapacitySamples,
			HeadroomTimeline:       headroomTimeline(d.Options.HeadroomTimeline),
			ReadinessOptions: metrics.ReadinessOptions{
				MinimumLookback:                   d.Options.Readiness.MinimumLookback.Duration,
				ExpectedResolution:                d.Options.Readiness.ExpectedResolution.Duration,
				DegradedMissingFraction:           d.Options.Readiness.DegradedMissingFraction,
				UnsupportedMissingFraction:        d.Options.Readiness.UnsupportedMissingFraction,
				DegradedResolutionMultiplier:      d.Options.Readiness.DegradedResolutionMultiplier,
				UnsupportedResolutionMultiplier:   d.Options.Readiness.UnsupportedResolutionMultiplier,
				DegradedGapMultiplier:             d.Options.Readiness.DegradedGapMultiplier,
				UnsupportedGapMultiplier:          d.Options.Readiness.UnsupportedGapMultiplier,
				MinimumWarmupSamplesToEstimate:    d.Options.Readiness.MinimumWarmupSamplesToEstimate,
				DemandStepChangeThreshold:         d.Options.Readiness.DemandStepChangeThreshold,
				DegradedDemandUnstableFraction:    d.Options.Readiness.DegradedDemandUnstableFraction,
				UnsupportedDemandUnstableFraction: d.Options.Readiness.UnsupportedDemandUnstableFraction,
			},
		},
	}

	snapshot := d.Snapshot
	if !ValidWindow(snapshot.Window) {
		snapshot.Window = InferredSnapshotWindow(snapshot, spec.Window, spec.Lookback)
	}
	return spec, snapshot
}

func replayKnownEvents(values []KnownEventDocument) []replay.KnownEvent {
	if len(values) == 0 {
		return nil
	}
	out := make([]replay.KnownEvent, 0, len(values))
	for _, value := range values {
		out = append(out, replay.KnownEvent{
			Name:  value.Name,
			Start: value.Start,
			End:   value.End,
			Note:  value.Note,
		})
	}
	return out
}

func dependencyStatuses(values []DependencyDocument) []safety.DependencyHealthStatus {
	if len(values) == 0 {
		return nil
	}
	out := make([]safety.DependencyHealthStatus, 0, len(values))
	for _, value := range values {
		out = append(out, safety.DependencyHealthStatus{
			Name:                value.Name,
			Healthy:             value.Healthy,
			HealthyRatio:        value.HealthyRatio,
			MinimumHealthyRatio: value.MinimumHealthyRatio,
			Message:             value.Message,
		})
	}
	return out
}

func headroomTimeline(values []HeadroomObservation) []replay.HeadroomObservation {
	if len(values) == 0 {
		return nil
	}
	out := make([]replay.HeadroomObservation, 0, len(values))
	for _, value := range values {
		out = append(out, replay.HeadroomObservation{
			ObservedAt: value.ObservedAt,
			Signal:     value.Signal,
		})
	}
	return out
}

func ValidWindow(window metrics.Window) bool {
	return !window.Start.IsZero() && !window.End.IsZero() && window.End.After(window.Start)
}

func InferredSnapshotWindow(snapshot metrics.Snapshot, replayWindow metrics.Window, lookback time.Duration) metrics.Window {
	start, end, ok := SeriesBounds(snapshot)
	if ok {
		return metrics.Window{Start: start, End: end}
	}

	if lookback <= 0 {
		lookback = 30 * time.Minute
	}
	return metrics.Window{
		Start: replayWindow.Start.Add(-lookback),
		End:   replayWindow.End,
	}
}

func SeriesBounds(snapshot metrics.Snapshot) (time.Time, time.Time, bool) {
	var start time.Time
	var end time.Time
	ok := false

	for _, series := range []*metrics.SignalSeries{
		&snapshot.Demand,
		&snapshot.Replicas,
		snapshot.CPU,
		snapshot.Memory,
		snapshot.Latency,
		snapshot.Errors,
		snapshot.Warmup,
		snapshot.NodeHeadroom,
	} {
		if series == nil || len(series.Samples) == 0 {
			continue
		}
		for _, sample := range series.Samples {
			if !ok || sample.Timestamp.Before(start) {
				start = sample.Timestamp
			}
			if !ok || sample.Timestamp.After(end) {
				end = sample.Timestamp
			}
			ok = true
		}
	}

	return start, end, ok
}
