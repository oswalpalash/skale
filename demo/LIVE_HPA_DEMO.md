# Live HPA Demo

This is the cluster-real demo path.

It exists for one purpose:

> capture an actual HPA-managed burst run in a live cluster, then replay and explain that captured history without inventing a baseline

Unlike the offline design-partner walkthrough, this path does **not** use a pre-generated synthetic replay fixture.

## What It Does

Recommended setup:

- use `make kind-up` so the live demo runs against a disposable `kind` cluster instead of a local distro with kubelet-specific quirks
- switch `kubectl` to `kind-skale` yourself before running the demo, or set `KIND_SWITCH_CONTEXT=1` when using `make kind-up`
- treat short `kind` runs as plumbing validation first: they prove real pod metrics, HPA observations, and replay capture without pretending that a few minutes of history is enough for a strong live recommendation
- use `make demo-live-hpa-learning` when you want a longer run where the first ~30 minutes is explicitly just the learning phase

`make demo-live-hpa`:

1. ensures the `PredictiveScalingPolicy` CRD is installed
2. checks `metrics.k8s.io` and refuses to continue if it is missing
3. applies a real demo `Deployment`, `Service`, `HPA`, and `PredictiveScalingPolicy`
4. waits for pod-level resource metrics and HPA resource metrics to become available
5. generates real burst traffic from an in-cluster load pod
6. captures:
   - injected request rate
   - observed ready replicas
   - observed CPU saturation ratio
   - observed memory saturation ratio
7. converts that capture into a replay-input JSON document
8. runs the recommendation-only controller against the captured history
9. runs `replayctl` against the same captured history

If you want the script to install metrics-server for you, rerun with:

```bash
INSTALL_METRICS_SERVER=1
```

The install path now uses a pinned release URL rather than `latest`.
Adding `--kubelet-insecure-tls` is also opt-in:

```bash
INSTALL_METRICS_SERVER=1 ALLOW_INSECURE_METRICS_SERVER=1
```

That flag is useful for some local clusters, but it is not enabled by default.

### Faster Local Runs

For a denser short-window `kind` demo, the script accepts runtime overrides.
Useful knobs include:

- `WORKLOAD_READINESS_DELAY_SECONDS`
- `POLICY_WARMUP_OVERRIDE`
- `POLICY_FORECAST_HORIZON_OVERRIDE`
- `POLICY_COOLDOWN_WINDOW_OVERRIDE`
- `HPA_SCALE_UP_STABILIZATION_SECONDS`
- `HPA_SCALE_DOWN_STABILIZATION_SECONDS`
- `STEP_SECONDS`
- `LOAD_SCHEDULE`
- `LOAD_REPEATS`

Example:

```bash
WORKLOAD_READINESS_DELAY_SECONDS=15 \
POLICY_WARMUP_OVERRIDE=30s \
POLICY_FORECAST_HORIZON_OVERRIDE=30s \
POLICY_COOLDOWN_WINDOW_OVERRIDE=30s \
HPA_SCALE_UP_STABILIZATION_SECONDS=0 \
HPA_SCALE_DOWN_STABILIZATION_SECONDS=30 \
STEP_SECONDS=10 \
LOAD_SCHEDULE=1,1,1,5,5,5,1,1,1,1,1,1 \
LOAD_REPEATS=3 \
LOOKBACK_DURATION=2m \
REPLAY_DURATION=2m \
FORECAST_HORIZON=30s \
FORECAST_SEASONALITY=30s \
WARMUP_DURATION=30s \
COOLDOWN_WINDOW=30s \
bash ./hack/demo-live-hpa.sh
```

That mode is still a cluster-real capture, but it is tuned for demo density rather than realistic warmup timing.

### Longer Learning-Phase Run

`make demo-live-hpa-learning` uses the same fast `kind` timings, but stretches the run long enough to show the conservative learning phase explicitly:

- capture cadence stays dense at `10s`
- the first `30m` is treated as telemetry learning
- replay/report generation then focuses on the trailing post-learning window, using the earlier `30m` only as required history
- the full live capture still stays on disk in CSV and replay-input form if you want to inspect the whole learning span separately

This run is intentionally slower than the fast smoke path. It is the honest way to show:

- real HPA movement on a live cluster
- an initial no-recommendation learning period
- later replay/controller behavior once enough history exists

## Important Honesty Boundary

This live path fails closed when the cluster cannot support it.

It also refuses to run on non-`kind` contexts by default.
If you want to bypass that safeguard, set:

```bash
SKALE_ALLOW_NON_KIND_CONTEXT=1
```

If pod-level resource metrics never become available to HPA, the script exits with an explicit unsupported message instead of inventing a reactive baseline.

That matters on some local clusters where:

- `metrics.k8s.io` may expose node metrics but not pod metrics
- `kubectl top pods` never becomes available
- HPA `.status.currentMetrics` remains empty or `unknown`

In that case, the cluster does **not** support a credible live HPA demo for this repo today.

The live script also retries transient `kubectl top` gaps during capture.
If metrics disappear for longer than that retry window, the run still fails closed rather than writing a partial baseline.

## Artifacts

The live demo writes artifacts under `./demo/output/live-hpa`:

- `live-hpa-samples.csv`
- `live-hpa-replay-input.json`
- `predictive-scaling-policy.yaml`
- `replay-summary.txt`
- `checkout-api-live-replay.md`
- `checkout-api-live-replay-report.json`
- `checkout-api-live-replay.html`

## Caveats

- The traffic is demo traffic, not organic production traffic.
- Warmup is still modeled as a fixed configured delay in replay.
- The replay input is derived from observed live samples, but replay remains a replay, not scheduler truth.
- If the cluster cannot provide real pod metrics to HPA, this path is unsupported by design.
- The live controller keeps its conservative readiness threshold. If the captured history is shorter than that threshold, controller status can remain `unsupported` even though the cluster-backed capture itself succeeded.
