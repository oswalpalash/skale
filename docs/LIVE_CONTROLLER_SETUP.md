# Live Controller Setup

This document is for operators who want to run the controller against live
Prometheus and Kubernetes state rather than demo fixtures.

## What This Path Does

The live controller:

- watches `PredictiveScalingPolicy` objects
- resolves the target `Deployment`
- detects a matching HPA when one exists
- reads workload telemetry from Prometheus
- reads request-based node-headroom inputs from the cluster API
- writes recommendation, suppression, and telemetry-readiness status back to the CRD

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
starts the controller with probes and leader election, but no Prometheus query
configuration.

If you want a pinned release instead of the rolling `main` image, update
[`config/manager/deployment.yaml`](../config/manager/deployment.yaml)
to a versioned tag such as `ghcr.io/oswalpalash/skale-controller:v0.1.0`
before applying manifests.

For local development, you can still build and load an unpublished image:

```bash
make docker-build IMAGE=ghcr.io/oswalpalash/skale-controller:dev VERSION=dev
kind load docker-image ghcr.io/oswalpalash/skale-controller:dev --name skale
```

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

## Controller Flags

The controller exposes the following live-telemetry flags:

- `--prometheus-url`
- `--prometheus-step`
- `--promql-demand`
- `--promql-replicas`
- `--promql-cpu`
- `--promql-memory`
- `--promql-warmup`
- `--promql-latency`
- `--promql-errors`
- `--promql-node-headroom`
- `--dependency-query-lookback`

Each query must already aggregate down to one normalized series for the target
workload. If a query returns multiple series, the controller treats that as a
configuration error instead of merging them implicitly.

The demand and replica queries may use:

- `$namespace`
- `$name`
- `$deployment`

## Example Deployment Args

Patch the controller deployment with your Prometheus base URL and queries. Keep
the expressions target-specific and pre-aggregated.

```yaml
args:
  - --leader-elect
  - --metrics-bind-address=:8080
  - --health-probe-bind-address=:8081
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
- `warmup.estimatedReadyDuration`
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

## Limitations

- The v1 controller is still recommendation-only.
- HPA presence is desirable for the target wedge, but target resolution does not
  currently fail just because no HPA was found.
- Node headroom is request-based and conservative. It does not model scheduling
  constraints such as affinity, taints, topology spread, or future node
  provisioning.
- Replay remains the best way to evaluate the value of predictive pre-scaling
  before trusting live recommendations.
