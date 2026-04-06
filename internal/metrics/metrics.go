package metrics

import (
	"context"
	"errors"
	"time"
)

var ErrNotImplemented = errors.New("metrics provider not implemented")

type SignalName string

const (
	SignalDemand       SignalName = "demand"
	SignalReplicas     SignalName = "replicas"
	SignalCPU          SignalName = "cpu"
	SignalMemory       SignalName = "memory"
	SignalLatency      SignalName = "latency"
	SignalErrors       SignalName = "errors"
	SignalWarmup       SignalName = "warmup"
	SignalNodeHeadroom SignalName = "nodeHeadroom"
)

// Target identifies the workload under evaluation.
type Target struct {
	Namespace string
	Name      string
}

// Window identifies a historical or live observation range.
type Window struct {
	Start time.Time
	End   time.Time
}

// Sample is a normalized point-in-time value.
type Sample struct {
	Timestamp time.Time
	Value     float64
}

// SignalSeries is a normalized workload signal with optional label-consistency metadata.
type SignalSeries struct {
	Name                    SignalName
	Samples                 []Sample
	ObservedLabelSignatures []string
	Unit                    string
}

// WorkloadSignals are the normalized workload-scoped signals used by readiness, replay, and recommendation flows.
//
// v1 requires demand and replica signals. CPU, memory, and warmup/readiness proxy signals are strongly recommended
// for determining whether a workload is actually supported. Latency and errors are optional enrichment signals.
type WorkloadSignals struct {
	Demand   SignalSeries
	Replicas SignalSeries
	CPU      *SignalSeries
	Memory   *SignalSeries
	Latency  *SignalSeries
	Errors   *SignalSeries
	Warmup   *SignalSeries
}

// ClusterSignals are the optional cluster-scoped signals used for safety checks.
type ClusterSignals struct {
	NodeHeadroom *SignalSeries
}

// Snapshot is the normalized telemetry shape consumed by readiness, replay, and recommendation stages.
type Snapshot struct {
	Window       Window
	Demand       SignalSeries
	Replicas     SignalSeries
	CPU          *SignalSeries
	Memory       *SignalSeries
	Latency      *SignalSeries
	Errors       *SignalSeries
	Warmup       *SignalSeries
	NodeHeadroom *SignalSeries
}

// Signal returns the requested normalized signal when present.
func (s Snapshot) Signal(name SignalName) *SignalSeries {
	switch name {
	case SignalDemand:
		return &s.Demand
	case SignalReplicas:
		return &s.Replicas
	case SignalCPU:
		return s.CPU
	case SignalMemory:
		return s.Memory
	case SignalLatency:
		return s.Latency
	case SignalErrors:
		return s.Errors
	case SignalWarmup:
		return s.Warmup
	case SignalNodeHeadroom:
		return s.NodeHeadroom
	default:
		return nil
	}
}

// WithClusterSignals combines workload and cluster signals into a normalized snapshot.
func (s WorkloadSignals) WithClusterSignals(window Window, cluster ClusterSignals) Snapshot {
	return Snapshot{
		Window:       window,
		Demand:       s.Demand,
		Replicas:     s.Replicas,
		CPU:          s.CPU,
		Memory:       s.Memory,
		Latency:      s.Latency,
		Errors:       s.Errors,
		Warmup:       s.Warmup,
		NodeHeadroom: cluster.NodeHeadroom,
	}
}

// Provider loads normalized workload telemetry.
type Provider interface {
	LoadWindow(ctx context.Context, target Target, window Window) (Snapshot, error)
}

// WorkloadFetcher loads workload-scoped signals such as demand, replicas, and pod readiness proxies.
type WorkloadFetcher interface {
	LoadWorkloadSignals(ctx context.Context, target Target, window Window) (WorkloadSignals, error)
}

// ClusterFetcher loads optional cluster-scoped safety signals such as node headroom.
type ClusterFetcher interface {
	LoadClusterSignals(ctx context.Context, target Target, window Window) (ClusterSignals, error)
}

// NoopProvider is a scaffold implementation used until real ingestion is added.
type NoopProvider struct{}

func (NoopProvider) LoadWindow(context.Context, Target, Window) (Snapshot, error) {
	return Snapshot{}, ErrNotImplemented
}
