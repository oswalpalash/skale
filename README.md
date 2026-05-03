# Skale

Skale is a recommendation-first controller and replay toolkit for one narrow
Kubernetes problem:

> would this workload have benefited from predictive pre-scaling, and if so,
> when, by how much, and under what safety constraints?

The repository is intentionally narrow in v1. It is not a generic autoscaling
platform, not a node autoscaler, and not autonomous control-plane software.

## Current v1 Scope

Supported today:

- cluster-wide discovery inventory for common Kubernetes workloads, with
  recommendation eligibility limited to explicit scaling contracts
- read-only workload qualification dashboard backed by discovery and policy
  status
- `PredictiveScalingPolicy` CRD in `skale.io/v1alpha1`
- recommendation-only controller status updates
- replay engine and offline report generation
- short-horizon forecasting using simple explainable models
- telemetry readiness evaluation
- bounded replica recommendation math
- explicit sizing assumptions, including policy-level `targetUtilization` and
  reported seasonality source
- suppression logic for confidence, telemetry quality, blackout windows,
  dependency health, known events, cooldown/stability, recent error, and node
  headroom
- Prometheus-backed live controller telemetry when queries are configured
- optional external TimesFM forecast runner, evaluated side by side with
  seasonal naive and Holt-Winters when configured
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
  recommendation-only controller, discovery publisher, and read-only
  qualification dashboard.
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

Install the CRD, namespace, RBAC, and controller:

```bash
kubectl apply -k ./config/default
```

Notes:

- the default deployment image is `ghcr.io/oswalpalash/skale-controller:main`
- for pinned releases, update
  [`config/manager/deployment.yaml`](./config/manager/deployment.yaml) to a
  versioned tag such as `ghcr.io/oswalpalash/skale-controller:v0.1.0`
- the default deployment starts the controller without Prometheus query flags
- without live telemetry flags, the controller still publishes discovery and
  reconciles policies, but discovery findings will mostly be
  `needs configuration` and policy telemetry readiness will remain
  `unsupported`
- the read-only dashboard is served on port `8082`; for local access:

```bash
kubectl port-forward -n skale-system svc/skale-dashboard 8082:8082
```

- pushes to `main` publish `ghcr.io/oswalpalash/skale-controller:main` and
  `ghcr.io/oswalpalash/skale-controller:sha-<commit>`
- release tags publish versioned images and refresh `ghcr.io/oswalpalash/skale-controller:latest`
- local development images are still useful when you are testing unpushed
  changes:

```bash
make docker-build IMAGE=ghcr.io/oswalpalash/skale-controller:dev VERSION=dev
kind load docker-image ghcr.io/oswalpalash/skale-controller:dev --name skale
```

See [`docs/LIVE_CONTROLLER_SETUP.md`](./docs/LIVE_CONTROLLER_SETUP.md)
for the operator-facing setup path and the exact telemetry contract.

## Cluster Discovery

Discovery is enabled by default. The controller scans all namespaces for common
workload controllers and HPAs, classifies each workload, and writes the current
inventory to a ConfigMap:

```bash
kubectl get configmap -n skale-system skale-discovery-inventory \
  -o jsonpath='{.data.summary\.txt}'

kubectl get configmap -n skale-system skale-discovery-inventory \
  -o jsonpath='{.data.inventory\.json}'
```

Discovery classifications are:

- `candidate`: HPA-managed Deployment with usable telemetry and enough burst or
  predictability evidence to run replay
- `needs configuration`: likely relevant, but missing telemetry query mapping,
  warmup, target utilization, or other policy context
- `needs scaling contract`: visible workload without an HPA or explicit Skale
  policy contract; no replica recommendation is surfaced
- `low confidence`: data exists, but forecast or burst evidence is weak
- `unsupported`: outside the current supported target types or telemetry is not
  usable

The same ConfigMap includes `policy-drafts.yaml` for candidate and
needs-configuration workloads. Those drafts are review material only. Full
recommendation and replay evaluation still require an explicit
`PredictiveScalingPolicy`; discovery never treats every workload as if it had a
safe per-workload policy.

Controller flags:

- `--cluster-discovery=false` disables inventory publishing
- `--discovery-namespace` changes where the inventory ConfigMap is written
- `--discovery-configmap` changes the ConfigMap name
- `--discovery-interval` changes the scan interval
- `--dashboard-bind-address=0` disables the dashboard

## Dashboard Behavior

The read-only dashboard is an operator evidence surface, not an actuation
surface. It starts at the namespace list, groups namespaces by whether they
contain policy-backed or candidate workloads, and exposes workload selection as
a collapsible namespace tree in the left rail. The workload detail, timeline,
and evidence stay in the main content area.

The workload timeline defaults to the last `30m`. Operators can widen the view
to `1h`, `3h`, or `6h`, and can hide or show the recommendation overlay with a
small graph checkbox. The selected namespace, workload, and time window are kept
in the URL hash so refreshes preserve context.

The same graph can show predicted replica overlays. Model demand forecasts are
converted through the workload policy, observed per-replica capacity,
`targetUtilization`, min/max replicas, and step bounds. TimesFM is included by
default when available, and seasonal naive / Holt-Winters can be toggled for
comparison. When the TimesFM runtime is not configured, the dashboard marks it
unavailable instead of drawing a fake line. Those overlays are evidence for the
recommendation path; they do not patch workload replicas.

Recommendation history comes from Prometheus, not from the CRD. The CRD keeps
only `.status.lastRecommendation`, which is intentionally the latest decision
summary. When Prometheus scrapes the controller metrics endpoint, the dashboard
queries historical `skale_recommendation_recommended_replicas` samples and
draws the recommendation path over time. If Prometheus has not scraped enough
points yet, the dashboard falls back to showing the latest recommendation as a
single point.

## Live Controller Telemetry Contract

For continuous live evaluation, configure the controller with:

- `--prometheus-url`
- `--promql-demand`
- `--promql-replicas`
- `--promql-cpu`
- `--promql-memory`

Additional optional flags:

- `--timesfm-url`
- `--timesfm-command`
- `--timesfm-timeout`
- `--promql-warmup`
- `--promql-latency`
- `--promql-errors`
- `--promql-node-headroom`
- `--dependency-query-lookback`

Important details:

- demand and replica queries are the minimum live-series contract
- CPU and memory are treated as required for readiness on this branch
- warmup may come from fixed policy configuration or a query-backed proxy
- when `--timesfm-url` or `--timesfm-command` is configured, TimesFM is the
  preferred forecast model; seasonal naive and Holt-Winters still run side by
  side for comparison
- without a TimesFM runtime, the controller keeps the in-process v1 forecasters
  and the dashboard marks TimesFM as unavailable instead of manufacturing a
  TimesFM line
- surfaced recommendation replicas are exported as controller Prometheus
  metrics only after telemetry is ready; learning-phase recommendations are not
  published as numeric recommendation samples
- dependency health checks use operator-supplied PromQL queries that must return
  one healthy-ratio series in the `0..1` range
- node headroom is a conservative request-based sanity check, not a scheduler or
  node autoscaler simulation

## Optional TimesFM Runner

The default controller image does not include Python, PyTorch, or TimesFM model
weights. For local Kubernetes demos, build and load the separate runner image:

```bash
make timesfm-docker-build
kind load docker-image ghcr.io/oswalpalash/skale-timesfm-runner:dev --name skale
kubectl apply -f demo/manifests/timesfm-runner.yaml
kubectl -n skale-system rollout status deploy/skale-timesfm-runner --timeout=15m
```

Then start the controller with:

```bash
--timesfm-url=http://skale-timesfm-runner.skale-system.svc:8080/forecast
```

The runner installs the upstream TimesFM repository with its `torch` extra and
keeps model loading out of the controller process.

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

Local live-dashboard observability:

- [`hack/setup-local-observability.sh`](./hack/setup-local-observability.sh)
  installs the local Prometheus and kube-state-metrics stack used by the
  dashboard demo
- [`hack/start-local-demo-traffic.sh`](./hack/start-local-demo-traffic.sh)
  starts deterministic jittered traffic for the checkout demo workload, so HPA
  and Skale telemetry show repeatable low/high phases instead of a permanently
  saturated service
- [`demo/manifests/local-observability.yaml`](./demo/manifests/local-observability.yaml)
  documents the exact scrape configuration
- this setup is intended for kind-based development only; production Prometheus
  labels and queries should be supplied by the operator

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
