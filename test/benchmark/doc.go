// Package benchmark contains a lightweight synthetic harness for replay and recommendation evaluation.
//
// The harness exists to make known patterns and failure modes repeatable:
// - recurring bursts that should benefit from predictive pre-scaling
// - one-off anomalies that should not be oversold as forecastable
// - telemetry gaps and cluster headroom limits that should fail closed
//
// It is intentionally not a general simulation framework. Scenarios generate normalized workload
// signals plus a practical observed replica baseline, then feed those fixtures through the same
// replay, forecast, recommendation, and safety paths used elsewhere in the repository.
package benchmark
