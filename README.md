# Skale

Skale is a recommendation-first controller and replay toolkit for one narrow
Kubernetes problem:

> would this workload have benefited from predictive pre-scaling, and if so,
> when, by how much, and under what safety constraints?

The repository is intentionally narrow in v1. It is not a generic autoscaling
platform, not a node autoscaler, and not autonomous control-plane software.

## Current v1 Scope

Supported today:

- `PredictiveScalingPolicy` CRD in `skale.io/v1alpha1`
- recommendation-only controller status updates
- replay engine and offline report generation
- short-horizon forecasting using simple explainable models
- telemetry readiness evaluation
- bounded replica recommendation math
- suppression logic for confidence, telemetry quality, blackout windows,
  dependency health, known events, cooldown/stability, recent error, and node
  headroom
- Prometheus-backed live controller telemetry when queries are configured
- request-based node-headroom sanity checks derived from live cluster state
- replay input fixtures for offline analysis and captured live-history replay

Out of scope in v1:

- default workload actuation
- node provisioning orchestration
- generalized KEDA or queue-first scaling
- opaque model pipelines
- broad cost-governance features
- multi-cluster management

## Repository Layout

- [`cmd/controller`](./cmd/controller) runs the
  recommendation-only controller.
- [`cmd/replayctl`](./cmd/replayctl) renders
  offline replay summaries, JSON, markdown, and self-contained HTML.
- [`config/default`](./config/default) contains a
  basic controller install path.
- [`config/samples`](./config/samples) contains a
  sample `PredictiveScalingPolicy`.
- [`demo`](./demo) contains the two supported demo
  walkthroughs.

## Install The Controller

The repository now includes a real controller deployment manifest instead of
placeholder config directories.

Build a local image:

```bash
make docker-build IMAGE=ghcr.io/oswalpalash/skale-controller:dev VERSION=dev
```

If you are using `kind`, load the image into the cluster:

```bash
kind load docker-image ghcr.io/oswalpalash/skale-controller:dev --name skale
```

Install the CRD, namespace, RBAC, and controller:

```bash
kubectl apply -k ./config/default
```

Notes:

- the default deployment image is `ghcr.io/oswalpalash/skale-controller:dev`
- for non-`kind` clusters, push that image to a registry your cluster can reach
  and update [`config/manager/deployment.yaml`](./config/manager/deployment.yaml)
- the default deployment starts the controller without Prometheus query flags
- without live telemetry flags, the controller still reconciles policies and
  writes status, but telemetry readiness will remain `unsupported`
- pushes to `main` publish `ghcr.io/oswalpalash/skale-controller:main` and
  `ghcr.io/oswalpalash/skale-controller:sha-<commit>`
- release tags publish versioned images and refresh `ghcr.io/oswalpalash/skale-controller:latest`

See [`docs/LIVE_CONTROLLER_SETUP.md`](./docs/LIVE_CONTROLLER_SETUP.md)
for the operator-facing setup path and the exact telemetry contract.

## Live Controller Telemetry Contract

For continuous live evaluation, configure the controller with:

- `--prometheus-url`
- `--promql-demand`
- `--promql-replicas`
- `--promql-cpu`
- `--promql-memory`

Additional optional flags:

- `--promql-warmup`
- `--promql-latency`
- `--promql-errors`
- `--promql-node-headroom`
- `--dependency-query-lookback`

Important details:

- demand and replica queries are the minimum live-series contract
- CPU and memory are treated as required for readiness on this branch
- warmup may come from fixed policy configuration or a query-backed proxy
- dependency health checks use operator-supplied PromQL queries that must return
  one healthy-ratio series in the `0..1` range
- node headroom is a conservative request-based sanity check, not a scheduler or
  node autoscaler simulation

## Safety Behavior

The controller is designed to fail closed. A recommendation may be unavailable
or suppressed when:

- telemetry is missing, unstable, stale, or too sparse
- forecast reliability is low
- recent forecast error is high
- models disagree beyond the allowed threshold
- a blackout window is active
- a known-event suppression window is active
- dependency health checks fail
- recent scaling activity or cooldown rules hold the system in a stability window
- node headroom is missing, uncertain, or insufficient for a scale-up path

The controller writes the current decision surface into CRD status rather than
patching workload replicas.

## Replay Caveats

Replay is evidence, not ground truth. Current replay behavior is intentionally
explicit about its limits:

- baseline behavior is reconstructed from the observed replica series, not from
  a full HPA algorithm simulation
- warmup is modeled as a fixed delay before recommended replicas are useful
- overload and excess-headroom outputs are proxy metrics, not latency or SLA
  guarantees
- scheduling effects beyond the current headroom checks are caveats, not modeled
  truth
- if replay cannot estimate the required-replica proxy in the replay window, it
  returns `unsupported` rather than manufacturing zero deltas

## Demos

Two demos are supported on this branch.

Offline design-partner walkthrough:

- [`demo/DESIGN_PARTNER_DEMO.md`](./demo/DESIGN_PARTNER_DEMO.md)
- uses a generated replay fixture for both controller demo mode and replayctl
- best for a repeatable, credibility-first walkthrough

Cluster-real live capture walkthrough:

- [`demo/LIVE_HPA_DEMO.md`](./demo/LIVE_HPA_DEMO.md)
- captures HPA-managed live history in-cluster, then converts it into replay input
- now refuses non-`kind` contexts by default unless you set
  `SKALE_ALLOW_NON_KIND_CONTEXT=1`
- no longer installs `metrics-server` implicitly; use `INSTALL_METRICS_SERVER=1`
  if you want the script to apply a pinned release manifest

## Development

Common commands:

```bash
make build
make test
make test-ci
make manifests
make docker-build IMAGE=ghcr.io/oswalpalash/skale-controller:dev VERSION=dev
```

`make test-ci` is the same path used in CI:

- `CGO_ENABLED=0 go test ./...`
- `CGO_ENABLED=0 go vet ./...`

The controller, replay CLI, livefixture tool, and demo fixture tool all expose
`-version`. Build metadata is injected through linker flags in the
[`Makefile`](./Makefile).

## Community And Process

- [`CONTRIBUTING.md`](./CONTRIBUTING.md)
- [`SECURITY.md`](./SECURITY.md)
- [`CODE_OF_CONDUCT.md`](./CODE_OF_CONDUCT.md)

The repository is still pre-1.0. Operator-facing docs should prefer explicit
limitations over broad claims.
