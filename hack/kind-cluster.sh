#!/usr/bin/env bash
set -euo pipefail

ACTION="${1:-up}"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-skale}"
WAIT_SECONDS="${KIND_WAIT_SECONDS:-180}"
CONTEXT="kind-${CLUSTER_NAME}"
KIND_SWITCH_CONTEXT="${KIND_SWITCH_CONTEXT:-0}"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

cluster_exists() {
  kind get clusters 2>/dev/null | grep -Fxq "${CLUSTER_NAME}"
}

require_command kind
require_command kubectl
require_command docker

maybe_switch_context() {
  if [[ "${KIND_SWITCH_CONTEXT}" == "1" ]]; then
    kubectl config use-context "${CONTEXT}" >/dev/null
    echo "Using kubectl context: ${CONTEXT}"
    return
  fi

  echo "kind cluster is ready: ${CONTEXT}"
  echo "Context was not switched automatically."
  echo "Run: kubectl config use-context ${CONTEXT}"
}

case "${ACTION}" in
  up)
    if ! cluster_exists; then
      kind create cluster --name "${CLUSTER_NAME}" --wait "${WAIT_SECONDS}s"
    fi
    maybe_switch_context
    kubectl get nodes -o wide
    ;;
  down)
    kind delete cluster --name "${CLUSTER_NAME}"
    ;;
  status)
    if cluster_exists; then
      maybe_switch_context
      kubectl get nodes -o wide
    else
      echo "kind cluster ${CLUSTER_NAME} does not exist" >&2
      exit 1
    fi
    ;;
  *)
    echo "usage: $0 [up|down|status]" >&2
    exit 1
    ;;
esac
