# Live Controller Setup

This document is for operators who want to run the controller against live
Prometheus and Kubernetes state rather than demo fixtures.

## What This Path Does

The live controller:

- scans all namespaces for common workload controllers and HPAs
- watches `PredictiveScalingPolicy` objects
- resolves the target `Deployment`
- detects a matching HPA when one exists
- reads workload telemetry from Prometheus
- reads request-based node-headroom inputs from the cluster API
- writes a cluster-wide discovery inventory ConfigMap
- writes recommendation, suppression, and telemetry-readiness status back to the CRD
- serves a read-only workload qualification dashboard from the same evidence

It does not patch workload replicas in v1.

## Install

The default manifest already points at the published rolling image:

`ghcr.io/oswalpalash/skale-controller:main`

Apply the install manifests:

```bash
kubectl apply -k ./config/default
```

The deployment in
[`config/manager/deployment.yaml`](../config/manager/deployment.yaml)
starts the controller with probes, leader election, and the read-only dashboard,
but no Prometheus query configuration.

Open the dashboard locally:

```bash
kubectl port-forward -n skale-system svc/skale-dashboard 8082:8082
```

Then visit `http://localhost:8082`.

If you want a pinned release instead of the rolling `main` image, update
[`config/manager/deployment.yaml`](../config/manager/deployment.yaml)
to a versioned tag such as `ghcr.io/oswalpalash/skale-controller:v0.1.0`
before applying manifests.

For local development, you can still build and load an unpublished image:

```bash
make docker-build IMAGE=ghcr.io/oswalpalash/skale-controller:dev VERSION=dev
kind load docker-image ghcr.io/oswalpalash/skale-controller:dev --name skale
```

## Local Kind Observability

For repeatable local dashboard testing, use the repo-owned setup script:

```bash
kubectl apply -f demo/manifests/checkout-api-live-demo.yaml
hack/setup-local-observability.sh
hack/start-local-demo-traffic.sh
kubectl port-forward -n skale-system svc/skale-dashboard 8082:8082
```

The script applies
[`demo/manifests/local-observability.yaml`](../demo/manifests/local-observability.yaml),
waits for Prometheus and kube-state-metrics, annotates the controller for
Prometheus scraping, and patches local demo PromQL into the controller
deployment.

This is a development convenience, not a production install path. It assumes the
demo app exposes `skale_demo_requests_total` and that the controller is running
in `skale-system`.

`hack/start-local-demo-traffic.sh` uses deterministic jitter by default. Tune
`JITTER_SEED`, `WORKER_SCHEDULE`, `MAX_EXTRA_WORKERS`, `PHASE_SECONDS`, and
`REQUEST_PERIOD_SECONDS` to generate repeatable traffic shapes while still
giving the model imperfect demand patterns to evaluate.

## Required Live Telemetry

The live controller expects all of the following before it can surface strong
recommendations:

- demand signal
- replica count signal
- CPU saturation signal
- memory saturation signal
- warmup duration from policy or a warmup proxy query

Optional enrichments:

- latency
- errors
- node headroom from Prometheus
- dependency health checks

Even though the Prometheus adapter only requires demand and replicas at query
configuration time, the current readiness pipeline still treats CPU and memory
as required for a `ready` workload.

## Discovery Inventory

Cluster discovery is enabled by default. It lists common workload controllers
across all namespaces, but recommendation eligibility remains limited to
workloads with an explicit scaling contract. Unsupported workload kinds are
visible so operators can see why Skale is withholding replica recommendations.

Inspect the latest inventory:

```bash
kubectl get configmap -n skale-system skale-discovery-inventory \
  -o jsonpath='{.data.summary\.txt}'

kubectl get configmap -n skale-system skale-discovery-inventory \
  -o jsonpath='{.data.inventory\.json}'
```

The inventory uses four classifications:

- `candidate`: HPA-managed Deployment with usable telemetry and enough burst or
  recurrence evidence to justify replay
- `needs configuration`: likely relevant, but missing telemetry query mapping,
  warmup, target utilization, or safety context
- `needs scaling contract`: visible Deployment without an HPA or explicit Skale
  policy contract; Skale withholds replica recommendations until the scaling
  envelope and safety context are explicit
- `low confidence`: telemetry exists, but replay or forecast evidence is weak
- `unsupported`: outside the current supported target types, missing required
  signals, or poor label quality

`policy-drafts.yaml` in the same ConfigMap contains conservative
`PredictiveScalingPolicy` drafts for review. Applying a draft is the explicit
step that turns discovery into policy-backed evaluation. Discovery itself does
not create recommendations or patch workload replicas.

## Controller Flags

The controller exposes the following live-telemetry flags:

- `--prometheus-url`
- `--prometheus-step`
- `--timesfm-url`
- `--timesfm-command`
- `--timesfm-timeout`
- `--promql-demand`
- `--promql-replicas`
- `--promql-cpu`
- `--promql-memory`
- `--promql-warmup`
- `--promql-latency`
- `--promql-errors`
- `--promql-node-headroom`
- `--dependency-query-lookback`
- `--cluster-discovery`
- `--discovery-namespace`
- `--discovery-configmap`
- `--discovery-interval`
- `--dashboard-bind-address`

Each query must already aggregate down to one normalized series for the target
workload. If a query returns multiple series, the controller treats that as a
configuration error instead of merging them implicitly.

The demand and replica queries may use:

- `$namespace`
- `$name`
- `$deployment`

## TimesFM Forecasting

Skale can use TimesFM as the preferred forecast model through an external
runtime. The default controller image stays Go-only. Python, PyTorch, model
weights, and checkpoint cache belong in a separate runner.

For Kubernetes demos, use the HTTP runner:

```bash
make timesfm-docker-build
kind load docker-image ghcr.io/oswalpalash/skale-timesfm-runner:dev --name skale
kubectl apply -f demo/manifests/timesfm-runner.yaml
kubectl -n skale-system rollout status deploy/skale-timesfm-runner --timeout=15m
kubectl -n skale-system set args deploy/skale-controller --containers=manager -- \
  --leader-elect \
  --metrics-bind-address=:8080 \
  --health-probe-bind-address=:8081 \
  --dashboard-bind-address=:8082 \
  --timesfm-url=http://skale-timesfm-runner.skale-system.svc:8080/forecast
```

Keep the existing Prometheus query flags when patching a live demo controller.
The command above shows only the TimesFM-specific runtime shape.

For custom images or local non-Kubernetes runs, the command runner is still
supported. The command receives JSON on stdin and returns JSON on stdout. The
helper at
[`hack/timesfm-forecast.py`](../hack/timesfm-forecast.py) implements that
protocol using the upstream TimesFM Python package:

```bash
--timesfm-command=/opt/skale/timesfm-forecast.py
```

When this flag is set, the controller evaluates TimesFM, seasonal naive, and
Holt-Winters side by side, but prefers TimesFM when it produces a usable result.
If TimesFM fails or is not configured, Skale fails closed or falls back according
to the existing model-selection path and surfaces the reason. The dashboard
marks TimesFM unavailable instead of drawing a fake TimesFM line. It also keeps
seasonal naive and Holt-Winters overlays available so the graph does not become
blank while the TimesFM runner is starting or misconfigured.

Dashboard model overlays are predicted replica counts, not raw demand units.
The controller converts each model forecast through the workload policy,
observed per-replica capacity, `targetUtilization`, min/max replicas, and step
bounds so operators can compare Skale's predicted replica path against the
current/HPA replica path.

Production packaging is intentionally explicit: use a separate runner service,
custom image, or controlled runtime path that can load the TimesFM dependencies
and checkpoint cache.

## Example Deployment Args

Patch the controller deployment with your Prometheus base URL and queries. Keep
the expressions target-specific and pre-aggregated.

```yaml
args:
  - --leader-elect
  - --metrics-bind-address=:8080
  - --health-probe-bind-address=:8081
  - --timesfm-url=http://skale-timesfm-runner.skale-system.svc:8080/forecast
  - --prometheus-url=http://prometheus.monitoring.svc:9090
  - --promql-demand=sum(rate(http_requests_total{namespace="$namespace",deployment="$deployment"}[5m]))
  - --promql-replicas=max(kube_deployment_status_replicas_available{namespace="$namespace",deployment="$deployment"})
  - --promql-cpu=max(rate(container_cpu_usage_seconds_total{namespace="$namespace",pod=~"$deployment-.*",container!="POD"}[5m])) / 0.25
  - --promql-memory=max(container_memory_working_set_bytes{namespace="$namespace",pod=~"$deployment-.*",container!="POD"}) / 268435456
```

These examples are only shape examples. The exact labels and denominators depend
on your metrics and workload resource requests.

## Policy Surface

The sample policy lives at
[`config/samples/skale.io_v1alpha1_predictivescalingpolicy.yaml`](../config/samples/skale.io_v1alpha1_predictivescalingpolicy.yaml).

Current notable fields:

- `targetRef`
- `forecastHorizon`
- `forecastSeasonality` when the operator has evidence for a recurring period;
  omit it to make the controller report either detected seasonality or
  non-seasonal mode
- `warmup.estimatedReadyDuration`
- `targetUtilization`, the utilization target used to convert demand forecasts
  into replica counts
- `confidenceThreshold`
- `minReplicas`
- `maxReplicas`
- `scaleUp` and `scaleDown`
- `cooldownWindow`
- `blackoutWindows`
- `knownEvents`
- `dependencyHealthChecks`
- `nodeHeadroomSanity`

## Status Expectations

Healthy live-controller setup should produce status fields under:

- `.status.observedWorkload`
- `.status.telemetryReadiness`
- `.status.lastForecast`
- `.status.lastRecommendation`
- `.status.suppressionReasons`
- `.status.conditions`

If telemetry is incomplete, the correct output is an explicit `unsupported` or
`degraded` state, not a forced recommendation.

The dashboard intentionally lists all discovered workloads, including workloads
that do not have a known scaling contract. Those workloads are shown as
`needs scaling contract` rather than receiving guessed replica counts.

The dashboard uses a collapsible namespace/workload tree in the left rail so
the selected workload detail and graph can use the main content area. The
timeline defaults to the last `30m`. Operators can widen the range to `1h`,
`3h`, or `6h`, and can hide or show the recommendation overlay with the small
timeline checkbox. The selected namespace, workload, and window are stored in
the URL hash so a refresh does not lose context.

Recommendation history is Prometheus-backed. The CRD status keeps only
`.status.lastRecommendation`, so a dashboard with no scraped recommendation
history can only draw one latest recommendation point. Once Prometheus is
scraping the controller metrics endpoint, the dashboard queries
`skale_recommendation_recommended_replicas` over the selected window and draws
the historical recommendation path. The controller does not export numeric
recommendation samples during telemetry learning or other telemetry-not-ready
states.

Discovery status is stored separately from policy status in the
`skale-discovery-inventory` ConfigMap. This separation is intentional:
cluster-wide discovery can identify candidates, but full recommendations require
the safety context encoded in a `PredictiveScalingPolicy`.

## Limitations

- The v1 controller is still recommendation-only.
- Discovery promotes only HPA-managed Deployments as v1 candidates. A policy can
  still reference a Deployment without an HPA, but that status should be treated
  as outside the recommended v1 trial path until an HPA exists.
- Node headroom is request-based and conservative. It does not model scheduling
  constraints such as affinity, taints, topology spread, or future node
  provisioning.
- Replay remains the best way to evaluate the value of predictive pre-scaling
  before trusting live recommendations.
