# Design-Partner Demo

This is the offline design-partner walkthrough.

This demo shows one supported v1 workload end to end:

- a real `Deployment`
- a real `HorizontalPodAutoscaler`
- a real `PredictiveScalingPolicy` in `recommendationOnly` mode
- one replay fixture that drives both the replay report and the controller's demo-only static metrics mode

The goal is clarity, not polish.
It is meant to answer: "What would this look like for one service if I trialed it safely?"

If you want a cluster-real HPA capture path instead, use `./demo/LIVE_HPA_DEMO.md`.

## Prerequisites

- Go installed locally
- `kubectl` pointed at a disposable cluster
- by default the script now requires a `kind-*` context
- if you really want to target a non-`kind` cluster, set `SKALE_ALLOW_NON_KIND_CONTEXT=1`

The demo uses:

- sample manifest: `./demo/manifests/checkout-api-demo.yaml`
- generated replay fixture: `./demo/output/checkout-api-replay-input.json`
- fixture generator: `./cmd/demofixture`
- script: `./hack/demo-design-partner.sh`

The script writes real Kubernetes resources into the current context. It will
refuse non-`kind` contexts unless you opt into that explicitly.

## Quick Start

From the repository root:

```bash
make demo-design-partner
```

The script will:

1. install the CRD
2. generate a richer 24-hour replay-input JSON under `./demo/output`
3. apply the demo namespace, `Deployment`, `HPA`, and `PredictiveScalingPolicy`
4. start the controller with `-demo-replay-input=./demo/output/checkout-api-replay-input.json`
5. print a note if the local cluster Metrics API is unavailable
6. wait for policy status to be written
7. run `replayctl` on the same generated fixture
8. write operator-facing artifacts under `./demo/output`

In demo mode, the controller evaluates the fixture at the fixture's latest timestamp.
That keeps recommendation timestamps aligned with the historical samples instead of the current wall clock.

## What To Inspect

### Cluster objects

The script prints:

- `Deployment`
- `HorizontalPodAutoscaler`
- `PredictiveScalingPolicy`

The most useful cluster artifact is:

- `./demo/output/predictive-scaling-policy.yaml`

Look at:

- `.status.observedWorkload`
- `.status.telemetryReadiness`
- `.status.lastForecast`
- `.status.lastRecommendation`
- `.status.conditions`

Those fields show the controller's recommendation-only posture.
The controller does not patch live replicas.

### Replay outputs

The script also writes:

- `./demo/output/checkout-api-replay-input.json`
- `./demo/output/replay-summary.txt`
- `./demo/output/checkout-api-replay.md`
- `./demo/output/checkout-api-replay-report.json`
- `./demo/output/checkout-api-replay.html`

Use them in this order:

1. `checkout-api-replay-input.json`
   The exact synthetic replay evidence used by both the controller demo mode and `replayctl`.
2. `replay-summary.txt`
   Short operator summary for a terminal session.
3. `checkout-api-replay.html`
   Single-view replay lifecycle artifact for the full 24-hour window.
4. `checkout-api-replay.md`
   Shareable design-partner report in plain markdown.
5. `checkout-api-replay-report.json`
   Full structured artifact for inspection or tooling.

## What The Demo Is Showing

The sample workload is a narrow v1 fit:

- HPA-managed `Deployment`
- recurring daily burst windows spread across a 24-hour view
- reactive HPA-style path that arrives late to burst clusters
- explicit 30-minute warmup lag
- recommendation-only policy

The generated replay fixture is constructed so that:

- the replay window covers a full 24-hour day view
- the demand pattern includes multiple burst windows with quieter overnight periods
- the recorded replica line is a lagged HPA-style baseline that reacts after bursts start
- the forecast has enough lookback to see the daily recurrence and pre-scale before repeated burst windows
- the controller can still surface a bounded recommendation at the tail of the fixture
- replay can compare that recommendation path to observed reactive behavior across the day, not only one short burst cycle

## How To Read The Output

### Telemetry readiness

If telemetry is `ready`, the fixture had enough demand, replica, CPU, and memory history for the current v1 checks.

If it is not `ready`, the correct interpretation is:

- the workload is currently unsupported or degraded for strong recommendation surfacing
- the system failed closed

### Recommendation summary

In controller status and replay events, focus on:

- current replicas
- recommended replicas
- forecast method
- confidence
- bounded result
- suppression reasons, if any

The recommendation is advisory.
It is evidence for operator review, not actuation.

With the generated 24-hour fixture, the controller summary should show:

- telemetry `ready`
- forecast method `seasonal_naive`
- recommendation state `available`
- recommended replicas `3` or `4`, depending on where the tail of the recurring burst lands inside the forecast horizon

### Replay summary

Focus on:

- recommendation event count
- suppression reason counts
- overload-minute proxy delta
- excess-headroom proxy delta
- caveats and confidence notes

In the HTML replay view, correlate these on one timeline:

- observed demand
- actual replicas from the recorded historical series; in this demo, that series reflects the lagged HPA-style path encoded in the generated fixture
- predictive replicas that would likely have been ready after warmup
- green warmup spans from surfaced evaluation time to predicted ready time
- amber held-check marks when a later evaluation did not surface because of a safety gate such as cooldown
- recommendation evaluation and activation spans
- suppressed evaluations

Those replay deltas are proxies, not SLA guarantees.
With the generated fixture, replay should show a full-day window with repeated predictive `2 -> 3` and `3 -> 4` pre-scaling cycles, plus a lower overload proxy than the reactive HPA-style baseline.

## Caveats And What Is Simulated

This demo is intentionally explicit about simulation boundaries.

What is real:

- the local Kubernetes cluster
- the namespace, `Deployment`, `HPA`, and `PredictiveScalingPolicy`
- the controller-runtime reconciliation loop
- the CRD status and condition updates
- the replay engine, recommendation engine, safety logic, and report serializers

What is simulated:

- the controller's metrics input
  It comes from one generated replay-input JSON fixture via `-demo-replay-input`, not a live Prometheus backend.
- the controller's evaluation time
  In demo mode it is pinned to the fixture's latest sample timestamp so the recommendation explanation stays consistent with the historical input.
- the controller's telemetry cadence and forecast seasonality assumptions
  In demo mode they are aligned to the replay fixture so the controller status reflects the same synthetic 24-hour pattern shown in replay, rather than the normal live-controller defaults.
- the workload demand history
  It is synthetic and chosen to reflect a recurring bursty API pattern over a full-day replay window.
- the replay baseline
  It is reconstructed from the observed replica series in the fixture, not from a full HPA algorithm simulation.
- node schedulability
  This demo still uses synthetic history and does not prove real scheduler placement, even though the controller now has a live request-based headroom gate on its non-demo path.
- live HPA scaling behavior
  The demo applies a real `HPA` object so the target shape is realistic, but the replay baseline is still the generated fixture. If the local cluster Metrics API is unavailable, the demo script prints that explicitly.

What this demo does not prove:

- that your cluster currently has enough telemetry quality
- that your production HPA will behave exactly like the fixture baseline
- that pods would have been scheduled in time under real node pressure
- that your Prometheus query wiring is correct for live-controller mode

## Cleaning Up

To remove the demo workload:

```bash
kubectl delete -f ./demo/manifests/checkout-api-demo.yaml --ignore-not-found
```

The CRD may remain installed.
The demo output files under `./demo/output` are safe to delete locally.
