#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT_DIR="${ROOT_DIR}/demo/output/live-hpa"
MANIFEST_PATH="${ROOT_DIR}/demo/manifests/checkout-api-live-demo.yaml"
CRD_PATH="${ROOT_DIR}/config/crd/bases/skale.io_predictivescalingpolicies.yaml"
NAMESPACE="skale-live-demo"
DEPLOYMENT_NAME="checkout-api"
POLICY_NAME="checkout-api-predictive"
LOADGEN_NAME="checkout-loadgen"
CAPTURE_CSV="${OUTPUT_DIR}/live-hpa-samples.csv"
INPUT_PATH="${OUTPUT_DIR}/live-hpa-replay-input.json"
STATUS_PATH="${OUTPUT_DIR}/predictive-scaling-policy.yaml"
SUMMARY_PATH="${OUTPUT_DIR}/replay-summary.txt"
MARKDOWN_PATH="${OUTPUT_DIR}/checkout-api-live-replay.md"
JSON_PATH="${OUTPUT_DIR}/checkout-api-live-replay-report.json"
UI_PATH="${OUTPUT_DIR}/checkout-api-live-replay.html"
CONTROLLER_LOG="${OUTPUT_DIR}/controller.log"
GO_RUN_PREFIX="${GO_RUN_PREFIX:-CGO_ENABLED=0}"
METRICS_SERVER_VERSION="${METRICS_SERVER_VERSION:-v0.7.2}"
METRICS_SERVER_MANIFEST_URL="${METRICS_SERVER_MANIFEST_URL:-https://github.com/kubernetes-sigs/metrics-server/releases/download/${METRICS_SERVER_VERSION}/components.yaml}"
INSTALL_METRICS_SERVER="${INSTALL_METRICS_SERVER:-0}"
ALLOW_INSECURE_METRICS_SERVER="${ALLOW_INSECURE_METRICS_SERVER:-0}"
LOADGEN_IMAGE="${LOADGEN_IMAGE:-curlimages/curl:8.8.0}"

CPU_REQUEST_MILLI="${CPU_REQUEST_MILLI:-100}"
MEMORY_REQUEST_BYTES="${MEMORY_REQUEST_BYTES:-134217728}"
STEP_SECONDS="${STEP_SECONDS:-30}"
LOAD_SCHEDULE_DEFAULT="${LOAD_SCHEDULE_DEFAULT:-1,1,1,1,4,4,4,1,1,1,1,1}"
LOAD_REPEATS_DEFAULT="${LOAD_REPEATS_DEFAULT:-4}"
TOP_RETRY_ATTEMPTS="${TOP_RETRY_ATTEMPTS:-12}"
TOP_RETRY_SLEEP_SECONDS="${TOP_RETRY_SLEEP_SECONDS:-2}"
LOOKBACK_DURATION="${LOOKBACK_DURATION:-12m}"
REPLAY_DURATION="${REPLAY_DURATION:-12m}"
FORECAST_HORIZON="${FORECAST_HORIZON:-2m}"
FORECAST_SEASONALITY="${FORECAST_SEASONALITY:-6m}"
WARMUP_DURATION="${WARMUP_DURATION:-90s}"
COOLDOWN_WINDOW="${COOLDOWN_WINDOW:-2m}"
UI_FOCUS_DURATION="${UI_FOCUS_DURATION:-20m}"
WORKLOAD_READINESS_DELAY_SECONDS="${WORKLOAD_READINESS_DELAY_SECONDS:-}"
POLICY_WARMUP_OVERRIDE="${POLICY_WARMUP_OVERRIDE:-}"
POLICY_FORECAST_HORIZON_OVERRIDE="${POLICY_FORECAST_HORIZON_OVERRIDE:-}"
POLICY_COOLDOWN_WINDOW_OVERRIDE="${POLICY_COOLDOWN_WINDOW_OVERRIDE:-}"
HPA_SCALE_UP_STABILIZATION_SECONDS="${HPA_SCALE_UP_STABILIZATION_SECONDS:-}"
HPA_SCALE_DOWN_STABILIZATION_SECONDS="${HPA_SCALE_DOWN_STABILIZATION_SECONDS:-}"
HPA_SCALE_POLICY_PODS="${HPA_SCALE_POLICY_PODS:-4}"
HPA_SCALE_POLICY_PERCENT="${HPA_SCALE_POLICY_PERCENT:-100}"
HPA_SCALE_POLICY_PERIOD_SECONDS="${HPA_SCALE_POLICY_PERIOD_SECONDS:-15}"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

current_context() {
  kubectl config current-context 2>/dev/null || true
}

ensure_safe_context() {
  local context
  context="$(current_context)"
  if [[ -z "${context}" ]]; then
    echo "kubectl current-context is empty; refusing to apply demo resources" >&2
    exit 1
  fi
  if [[ "${SKALE_ALLOW_NON_KIND_CONTEXT:-0}" != "1" && "${context}" != kind-* ]]; then
    echo "refusing to run the live demo on non-kind context: ${context}" >&2
    echo "Use a disposable kind cluster, or rerun with SKALE_ALLOW_NON_KIND_CONTEXT=1 if intentional." >&2
    exit 1
  fi
  echo "Using kubectl context: ${context}"
}

cleanup() {
  if [[ -n "${CONTROLLER_PID:-}" ]]; then
    kill "${CONTROLLER_PID}" >/dev/null 2>&1 || true
    wait "${CONTROLLER_PID}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

parse_cpu_milli() {
  local value="$1"
  case "${value}" in
    *m) echo "${value%m}" ;;
    *) awk -v value="${value}" 'BEGIN { printf "%.0f", value * 1000 }' ;;
  esac
}

parse_memory_bytes() {
  local value="$1"
  case "${value}" in
    *Ki) awk -v value="${value%Ki}" 'BEGIN { printf "%.0f", value * 1024 }' ;;
    *Mi) awk -v value="${value%Mi}" 'BEGIN { printf "%.0f", value * 1024 * 1024 }' ;;
    *Gi) awk -v value="${value%Gi}" 'BEGIN { printf "%.0f", value * 1024 * 1024 * 1024 }' ;;
    *Ti) awk -v value="${value%Ti}" 'BEGIN { printf "%.0f", value * 1024 * 1024 * 1024 * 1024 }' ;;
    *) echo "${value}" ;;
  esac
}

current_hpa_target() {
  kubectl get hpa "${DEPLOYMENT_NAME}-hpa" -n "${NAMESPACE}" \
    -o jsonpath='{.status.currentMetrics[0].resource.current.averageUtilization}' 2>/dev/null || true
}

metrics_api_available() {
  kubectl get --raw /apis/metrics.k8s.io/v1beta1/nodes >/dev/null 2>&1
}

ensure_metrics_server() {
  if metrics_api_available; then
    return
  fi

  if [[ "${INSTALL_METRICS_SERVER}" != "1" ]]; then
    echo "metrics.k8s.io is unavailable on the current cluster" >&2
    echo "Install metrics-server yourself, or rerun with INSTALL_METRICS_SERVER=1 to apply ${METRICS_SERVER_MANIFEST_URL}" >&2
    echo "This script no longer installs metrics-server implicitly." >&2
    exit 1
  fi

  echo "Installing metrics-server from ${METRICS_SERVER_MANIFEST_URL}"
  kubectl apply -f "${METRICS_SERVER_MANIFEST_URL}" >/dev/null

  local args
  args="$(kubectl -n kube-system get deployment metrics-server -o jsonpath='{.spec.template.spec.containers[0].args}' 2>/dev/null || true)"
  if [[ "${ALLOW_INSECURE_METRICS_SERVER}" == "1" && "${args}" != *"--kubelet-insecure-tls"* ]]; then
    kubectl -n kube-system patch deployment metrics-server --type='json' \
      -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]' >/dev/null
  fi

  kubectl -n kube-system rollout status deployment/metrics-server --timeout=180s >/dev/null

  for _ in $(seq 1 30); do
    if metrics_api_available; then
      return
    fi
    sleep 2
  done

  echo "metrics-server installed but metrics.k8s.io is still unavailable" >&2
  exit 1
}

wait_for_live_hpa_support() {
  echo "Waiting for pod metrics and HPA resource metrics"
  for _ in $(seq 1 24); do
    if kubectl top pods -n "${NAMESPACE}" -l "app.kubernetes.io/name=${DEPLOYMENT_NAME}" >/dev/null 2>&1; then
      local current_target
      current_target="$(current_hpa_target)"
      if [[ -n "${current_target}" ]]; then
        return
      fi
    fi
    sleep 5
  done

  echo "live HPA demo unsupported on this cluster" >&2
  echo "The cluster is not exposing pod-level resource metrics to HPA." >&2
  echo "This demo refuses to fake an HPA baseline when .status.currentMetrics remains empty or kubectl top pods never becomes available." >&2
  echo "Check metrics.k8s.io pod metrics support on the target cluster and rerun." >&2
  exit 1
}

apply_runtime_overrides() {
  local patched=0

  if [[ -n "${WORKLOAD_READINESS_DELAY_SECONDS}" ]]; then
    kubectl patch deployment "${DEPLOYMENT_NAME}" -n "${NAMESPACE}" --type='json' \
      -p="[{
        \"op\":\"replace\",
        \"path\":\"/spec/template/spec/containers/0/readinessProbe/initialDelaySeconds\",
        \"value\":${WORKLOAD_READINESS_DELAY_SECONDS}
      }]" >/dev/null
    patched=1
  fi

  if [[ -n "${POLICY_WARMUP_OVERRIDE}" || -n "${POLICY_FORECAST_HORIZON_OVERRIDE}" || -n "${POLICY_COOLDOWN_WINDOW_OVERRIDE}" ]]; then
    local policy_patch='{"spec":{'
    local separator=""
    if [[ -n "${POLICY_WARMUP_OVERRIDE}" ]]; then
      policy_patch+="${separator}\"warmup\":{\"estimatedReadyDuration\":\"${POLICY_WARMUP_OVERRIDE}\"}"
      separator=","
    fi
    if [[ -n "${POLICY_FORECAST_HORIZON_OVERRIDE}" ]]; then
      policy_patch+="${separator}\"forecastHorizon\":\"${POLICY_FORECAST_HORIZON_OVERRIDE}\""
      separator=","
    fi
    if [[ -n "${POLICY_COOLDOWN_WINDOW_OVERRIDE}" ]]; then
      policy_patch+="${separator}\"cooldownWindow\":\"${POLICY_COOLDOWN_WINDOW_OVERRIDE}\""
    fi
    policy_patch+='}}'
    kubectl patch predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" --type='merge' -p "${policy_patch}" >/dev/null
    patched=1
  fi

  if [[ -n "${HPA_SCALE_UP_STABILIZATION_SECONDS}" || -n "${HPA_SCALE_DOWN_STABILIZATION_SECONDS}" ]]; then
    local scale_up_stabilization="${HPA_SCALE_UP_STABILIZATION_SECONDS:-0}"
    local scale_down_stabilization="${HPA_SCALE_DOWN_STABILIZATION_SECONDS:-300}"
    kubectl patch hpa "${DEPLOYMENT_NAME}-hpa" -n "${NAMESPACE}" --type='merge' \
      -p="{
        \"spec\":{
          \"behavior\":{
            \"scaleUp\":{
              \"stabilizationWindowSeconds\":${scale_up_stabilization},
              \"selectPolicy\":\"Max\",
              \"policies\":[
                {\"type\":\"Pods\",\"value\":${HPA_SCALE_POLICY_PODS},\"periodSeconds\":${HPA_SCALE_POLICY_PERIOD_SECONDS}},
                {\"type\":\"Percent\",\"value\":${HPA_SCALE_POLICY_PERCENT},\"periodSeconds\":${HPA_SCALE_POLICY_PERIOD_SECONDS}}
              ]
            },
            \"scaleDown\":{
              \"stabilizationWindowSeconds\":${scale_down_stabilization},
              \"selectPolicy\":\"Max\",
              \"policies\":[
                {\"type\":\"Pods\",\"value\":${HPA_SCALE_POLICY_PODS},\"periodSeconds\":${HPA_SCALE_POLICY_PERIOD_SECONDS}},
                {\"type\":\"Percent\",\"value\":${HPA_SCALE_POLICY_PERCENT},\"periodSeconds\":${HPA_SCALE_POLICY_PERIOD_SECONDS}}
              ]
            }
          }
        }
      }" >/dev/null
    patched=1
  fi

  if (( patched == 1 )); then
    echo "Applied live demo runtime overrides"
  fi
}

capture_resource_ratios() {
  local top_output
  top_output=""
  for _ in $(seq 1 "${TOP_RETRY_ATTEMPTS}"); do
    top_output="$(kubectl top pods -n "${NAMESPACE}" -l "app.kubernetes.io/name=${DEPLOYMENT_NAME}" --no-headers 2>/dev/null || true)"
    if [[ -n "${top_output}" ]]; then
      break
    fi
    sleep "${TOP_RETRY_SLEEP_SECONDS}"
  done

  if [[ -z "${top_output}" ]]; then
    echo "lost pod metrics during live capture after ${TOP_RETRY_ATTEMPTS} retries" >&2
    return 1
  fi

  local pod_count=0
  local cpu_milli_total=0
  local memory_bytes_total=0
  while read -r _ cpu memory; do
    [[ -z "${cpu:-}" || -z "${memory:-}" ]] && continue
    cpu_milli_total=$((cpu_milli_total + $(parse_cpu_milli "${cpu}")))
    memory_bytes_total=$((memory_bytes_total + $(parse_memory_bytes "${memory}")))
    pod_count=$((pod_count + 1))
  done <<< "${top_output}"

  if (( pod_count == 0 )); then
    echo "kubectl top returned no pod rows during live capture after ${TOP_RETRY_ATTEMPTS} retries" >&2
    return 1
  fi

  local cpu_ratio
  local memory_ratio
  cpu_ratio="$(awk -v sum="${cpu_milli_total}" -v req="$((pod_count * CPU_REQUEST_MILLI))" 'BEGIN { if (req <= 0) { print "0.0000" } else { printf "%.4f", sum / req } }')"
  memory_ratio="$(awk -v sum="${memory_bytes_total}" -v req="$((pod_count * MEMORY_REQUEST_BYTES))" 'BEGIN { if (req <= 0) { print "0.0000" } else { printf "%.4f", sum / req } }')"
  printf '%s,%s\n' "${cpu_ratio}" "${memory_ratio}"
}

run_load_step() {
  local qps="$1"
  local duration="$2"

  if (( qps <= 0 )); then
    sleep "${duration}"
    echo 0
    return
  fi

  kubectl exec -n "${NAMESPACE}" "${LOADGEN_NAME}" -- sh -ceu "
    service='http://${DEPLOYMENT_NAME}.${NAMESPACE}.svc.cluster.local/'
    duration=${duration}
    workers=${qps}
    rm -f /tmp/skale-load-count.*
    i=1
    while [ \$i -le \$workers ]; do
      (
        end=\$((\$(date +%s) + duration))
        count=0
        while [ \$(date +%s) -lt \$end ]; do
          if curl -fsS --max-time 4 \"\$service\" >/dev/null; then
            count=\$((count + 1))
          fi
          sleep 1
        done
        echo \$count > /tmp/skale-load-count.\$i
      ) &
      i=\$((i + 1))
    done
    wait
    total=0
    for file in /tmp/skale-load-count.*; do
      [ -f \"\$file\" ] || continue
      value=\$(cat \"\$file\")
      total=\$((total + value))
      rm -f \"\$file\"
    done
    echo \"\$total\"
  "
}

require_command go
require_command kubectl
require_command awk

mkdir -p "${OUTPUT_DIR}"
cd "${ROOT_DIR}"

ensure_safe_context
ensure_metrics_server

echo "Installing PredictiveScalingPolicy CRD"
kubectl apply -f "${CRD_PATH}" >/dev/null

echo "Applying live HPA demo workload"
kubectl apply -f "${MANIFEST_PATH}" >/dev/null
apply_runtime_overrides

echo "Waiting for live demo Deployment rollout"
kubectl rollout status deployment/"${DEPLOYMENT_NAME}" -n "${NAMESPACE}" --timeout=240s >/dev/null

echo "Preparing in-cluster load generator"
kubectl delete pod "${LOADGEN_NAME}" -n "${NAMESPACE}" --ignore-not-found >/dev/null 2>&1 || true
kubectl run "${LOADGEN_NAME}" -n "${NAMESPACE}" --image="${LOADGEN_IMAGE}" --restart=Never --command -- sh -c 'sleep 7200' >/dev/null
kubectl wait --for=condition=Ready pod/"${LOADGEN_NAME}" -n "${NAMESPACE}" --timeout=240s >/dev/null

wait_for_live_hpa_support

echo "Capturing live load, replicas, and resource ratios"
printf 'timestamp,demand_qps,ready_replicas,cpu_ratio,memory_ratio\n' > "${CAPTURE_CSV}"

declare -a SCHEDULE=()
IFS=',' read -r -a LOAD_PATTERN <<< "${LOAD_SCHEDULE:-${LOAD_SCHEDULE_DEFAULT}}"
LOAD_REPEATS="${LOAD_REPEATS:-${LOAD_REPEATS_DEFAULT}}"
for _ in $(seq 1 "${LOAD_REPEATS}"); do
  SCHEDULE+=("${LOAD_PATTERN[@]}")
done

for index in "${!SCHEDULE[@]}"; do
  target_qps="${SCHEDULE[${index}]}"
  printf 'step %02d/%02d target=%sqps\n' "$((index + 1))" "${#SCHEDULE[@]}" "${target_qps}"
  total_requests="$(run_load_step "${target_qps}" "${STEP_SECONDS}")"
  observed_qps="$(awk -v total="${total_requests}" -v seconds="${STEP_SECONDS}" 'BEGIN { printf "%.4f", total / seconds }')"
  ready_replicas="$(kubectl get deployment "${DEPLOYMENT_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)"
  ready_replicas="${ready_replicas:-0}"
  if ! resource_ratios="$(capture_resource_ratios)"; then
    exit 1
  fi
  IFS=',' read -r cpu_ratio memory_ratio <<< "${resource_ratios}"
  timestamp="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  printf '%s,%s,%s,%s,%s\n' "${timestamp}" "${observed_qps}" "${ready_replicas}" "${cpu_ratio}" "${memory_ratio}" >> "${CAPTURE_CSV}"
done

echo "Building replay input from captured live history"
env ${GO_RUN_PREFIX} go run ./cmd/livefixture \
  -input "${CAPTURE_CSV}" \
  -out "${INPUT_PATH}" \
  -namespace "${NAMESPACE}" \
  -name "${DEPLOYMENT_NAME}" \
  -workload "${NAMESPACE}/${DEPLOYMENT_NAME}" \
  -step "${STEP_SECONDS}s" \
  -replay-duration "${REPLAY_DURATION}" \
  -lookback "${LOOKBACK_DURATION}" \
  -forecast-horizon "${FORECAST_HORIZON}" \
  -forecast-seasonality "${FORECAST_SEASONALITY}" \
  -warmup "${WARMUP_DURATION}" \
  -target-utilization 0.8 \
  -confidence-threshold 0.65 \
  -min-replicas 2 \
  -max-replicas 6 \
  -max-step-up 2 \
  -max-step-down 1 \
  -cooldown-window "${COOLDOWN_WINDOW}" >/dev/null

echo "Starting recommendation-only controller against the captured live replay input"
env ${GO_RUN_PREFIX} go run ./cmd/controller \
  -metrics-bind-address=0 \
  -health-probe-bind-address=0 \
  -demo-replay-input="${INPUT_PATH}" \
  >"${CONTROLLER_LOG}" 2>&1 &
CONTROLLER_PID=$!

sleep 2
if ! kill -0 "${CONTROLLER_PID}" >/dev/null 2>&1; then
  echo "controller exited before reconciliation completed" >&2
  cat "${CONTROLLER_LOG}" >&2
  exit 1
fi

echo "Waiting for predictive policy status"
for _ in $(seq 1 30); do
  recommendation_state="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastRecommendation.outcome.state}' 2>/dev/null || true)"
  if [[ -n "${recommendation_state}" ]]; then
    break
  fi
  recommendation_state="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastRecommendation.state}' 2>/dev/null || true)"
  if [[ -n "${recommendation_state}" ]]; then
    break
  fi
  sleep 1
done

if [[ -z "${recommendation_state:-}" ]]; then
  echo "predictive policy status did not appear within 30 seconds" >&2
  cat "${CONTROLLER_LOG}" >&2
  kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o yaml || true
  exit 1
fi

kubectl get deployment,hpa,predictivescalingpolicy -n "${NAMESPACE}"
kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o yaml > "${STATUS_PATH}"

echo
echo "Controller recommendation summary"
telemetry_state="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.telemetryReadiness.state}' 2>/dev/null || true)"
forecast_method="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastForecast.method}' 2>/dev/null || true)"
forecast_confidence="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastForecast.confidence}' 2>/dev/null || true)"
recommendation_message="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastRecommendation.outcome.message}' 2>/dev/null || true)"
if [[ -z "${recommendation_message}" ]]; then
  recommendation_message="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastRecommendation.message}' 2>/dev/null || true)"
fi
printf 'Deployment/%s\n' "${DEPLOYMENT_NAME}"
printf 'telemetry: %s\n' "${telemetry_state:-unknown}"
printf 'forecast method: %s\n' "${forecast_method:-unknown}"
printf 'forecast confidence: %s\n' "${forecast_confidence:-unknown}"
printf 'recommendation state: %s\n' "${recommendation_state:-unknown}"
printf 'recommended replicas: %s\n' "$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastRecommendation.outcome.finalRecommendedReplicas}' 2>/dev/null || kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastRecommendation.recommendedReplicas}' 2>/dev/null || true)"
printf 'message: %s\n' "${recommendation_message:-unknown}"
printf 'learning phase: first %s of captured history is telemetry learning; strong recommendations before that point may remain unavailable or suppressed.\n' "${LOOKBACK_DURATION}"

echo
echo "Running replayctl against the captured live replay input"
env ${GO_RUN_PREFIX} go run ./cmd/replayctl \
  -input "${INPUT_PATH}" \
  -ui-focus "${UI_FOCUS_DURATION}" \
  -json-out "${JSON_PATH}" \
  -markdown-out "${MARKDOWN_PATH}" \
  -ui-out "${UI_PATH}" \
  > "${SUMMARY_PATH}"
cat "${SUMMARY_PATH}"

echo
echo "Artifacts written to:"
echo "  ${CAPTURE_CSV}"
echo "  ${INPUT_PATH}"
echo "  ${STATUS_PATH}"
echo "  ${SUMMARY_PATH}"
echo "  ${MARKDOWN_PATH}"
echo "  ${JSON_PATH}"
echo "  ${UI_PATH}"
echo "  ${CONTROLLER_LOG}"
