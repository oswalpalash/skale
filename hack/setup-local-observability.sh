#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OBSERVABILITY_MANIFEST="${ROOT_DIR}/demo/manifests/local-observability.yaml"
PROMETHEUS_URL="${PROMETHEUS_URL:-http://prometheus.demo-monitoring.svc:9090}"
CONTROLLER_NAMESPACE="${CONTROLLER_NAMESPACE:-skale-system}"
CONTROLLER_DEPLOYMENT="${CONTROLLER_DEPLOYMENT:-skale-controller}"

kubectl apply -f "${OBSERVABILITY_MANIFEST}"
kubectl -n demo-monitoring rollout status deployment/kube-state-metrics --timeout=180s
kubectl -n demo-monitoring rollout status deployment/prometheus --timeout=180s

kubectl -n "${CONTROLLER_NAMESPACE}" patch deployment "${CONTROLLER_DEPLOYMENT}" --type='merge' -p='{
  "spec": {
    "template": {
      "metadata": {
        "annotations": {
          "prometheus.io/scrape": "true",
          "prometheus.io/port": "8080",
          "prometheus.io/path": "/metrics"
        }
      }
    }
  }
}'

kubectl -n "${CONTROLLER_NAMESPACE}" patch deployment "${CONTROLLER_DEPLOYMENT}" --type='json' -p="[
  {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/args\",\"value\":[
    \"--leader-elect\",
    \"--metrics-bind-address=:8080\",
    \"--health-probe-bind-address=:8081\",
    \"--dashboard-bind-address=:8082\",
    \"--prometheus-url=${PROMETHEUS_URL}\",
    \"--prometheus-step=10s\",
    \"--promql-demand=sum(rate(skale_demo_requests_total{namespace=\\\"\\\$namespace\\\",deployment=\\\"\\\$deployment\\\"}[1m]))\",
    \"--promql-replicas=max(kube_deployment_status_replicas_available{namespace=\\\"\\\$namespace\\\",deployment=\\\"\\\$deployment\\\"})\",
    \"--promql-cpu=sum(rate(container_cpu_usage_seconds_total{namespace=\\\"\\\$namespace\\\",pod=~\\\"\\\$deployment-.*\\\",container!=\\\"\\\",container!=\\\"POD\\\"}[1m])) / (max(kube_deployment_status_replicas_available{namespace=\\\"\\\$namespace\\\",deployment=\\\"\\\$deployment\\\"}) * 0.1)\",
    \"--promql-memory=sum(container_memory_working_set_bytes{namespace=\\\"\\\$namespace\\\",pod=~\\\"\\\$deployment-.*\\\",container!=\\\"\\\",container!=\\\"POD\\\"}) / (max(kube_deployment_status_replicas_available{namespace=\\\"\\\$namespace\\\",deployment=\\\"\\\$deployment\\\"}) * 134217728)\",
    \"--discovery-interval=15s\"
  ]}
]"
kubectl -n "${CONTROLLER_NAMESPACE}" rollout status deployment/"${CONTROLLER_DEPLOYMENT}" --timeout=180s

cat <<EOF
Local observability ready.

Prometheus: ${PROMETHEUS_URL}
Controller: ${CONTROLLER_NAMESPACE}/${CONTROLLER_DEPLOYMENT}

The live demo app must expose skale_demo_requests_total. Reapply:
  kubectl apply -f demo/manifests/checkout-api-live-demo.yaml
EOF
