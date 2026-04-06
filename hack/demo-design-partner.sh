#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT_DIR="${ROOT_DIR}/demo/output"
FIXTURE_PATH="${OUTPUT_DIR}/checkout-api-replay-input.json"
MANIFEST_PATH="${ROOT_DIR}/demo/manifests/checkout-api-demo.yaml"
CRD_PATH="${ROOT_DIR}/config/crd/bases/skale.io_predictivescalingpolicies.yaml"
NAMESPACE="skale-demo"
POLICY_NAME="checkout-api-predictive"
CONTROLLER_LOG="${OUTPUT_DIR}/controller.log"
STATUS_PATH="${OUTPUT_DIR}/predictive-scaling-policy.yaml"
SUMMARY_PATH="${OUTPUT_DIR}/replay-summary.txt"
MARKDOWN_PATH="${OUTPUT_DIR}/checkout-api-replay.md"
JSON_PATH="${OUTPUT_DIR}/checkout-api-replay-report.json"
UI_PATH="${OUTPUT_DIR}/checkout-api-replay.html"

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
    echo "refusing to apply demo resources to non-kind context: ${context}" >&2
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

require_command go
require_command kubectl

mkdir -p "${OUTPUT_DIR}"

cd "${ROOT_DIR}"

ensure_safe_context
if ! kubectl top pods -A >/dev/null 2>&1; then
  echo "Note: cluster Metrics API is unavailable. The HPA object is real, but live HPA history is not the baseline for this demo."
fi
echo "Generating richer 24-hour replay input"
go run ./cmd/demofixture -out "${FIXTURE_PATH}" >/dev/null
echo "Installing PredictiveScalingPolicy CRD"
kubectl apply -f "${CRD_PATH}" >/dev/null

echo "Resetting the demo PredictiveScalingPolicy for a fresh reconcile"
kubectl delete predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" --ignore-not-found >/dev/null 2>&1 || true

echo "Applying demo workload, HPA, and predictive policy"
kubectl apply -f "${MANIFEST_PATH}" >/dev/null

echo "Waiting for the demo Deployment to roll out"
kubectl rollout status deployment/checkout-api -n "${NAMESPACE}" --timeout=120s >/dev/null

echo "Starting recommendation-only controller in demo mode"
go run ./cmd/controller \
  -metrics-bind-address=0 \
  -health-probe-bind-address=0 \
  -demo-replay-input="${FIXTURE_PATH}" \
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
kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o yaml >"${STATUS_PATH}"

echo
echo "Controller recommendation summary"
workload_kind="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.observedWorkload.kind}' 2>/dev/null || true)"
workload_name="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.observedWorkload.name}' 2>/dev/null || true)"
telemetry_state="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.telemetryReadiness.state}' 2>/dev/null || true)"
forecast_method="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastForecast.method}' 2>/dev/null || true)"
forecast_confidence="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastForecast.confidence}' 2>/dev/null || true)"
recommendation_message="$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastRecommendation.message}' 2>/dev/null || true)"
printf '%s/%s\n' "${workload_kind:-unknown}" "${workload_name:-unknown}"
printf 'telemetry: %s\n' "${telemetry_state:-unknown}"
printf 'forecast method: %s\n' "${forecast_method:-unknown}"
printf 'forecast confidence: %s\n' "${forecast_confidence:-unknown}"
printf 'recommendation state: %s\n' "${recommendation_state:-unknown}"
printf 'recommended replicas: %s\n' "$(kubectl get predictivescalingpolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastRecommendation.recommendedReplicas}' 2>/dev/null || true)"
printf 'message: %s\n' "${recommendation_message:-unknown}"

echo
echo "Running replayctl against the same generated fixture"
go run ./cmd/replayctl \
  -input "${FIXTURE_PATH}" \
  -ui-focus 24h \
  -json-out "${JSON_PATH}" \
  -markdown-out "${MARKDOWN_PATH}" \
  -ui-out "${UI_PATH}" \
  >"${SUMMARY_PATH}"
cat "${SUMMARY_PATH}"

echo
echo "Artifacts written to:"
echo "  ${STATUS_PATH}"
echo "  ${SUMMARY_PATH}"
echo "  ${MARKDOWN_PATH}"
echo "  ${JSON_PATH}"
echo "  ${UI_PATH}"
echo "  ${FIXTURE_PATH}"
echo "  ${CONTROLLER_LOG}"
