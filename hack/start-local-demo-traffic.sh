#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-skale-live-demo}"
SERVICE="${SERVICE:-checkout-api}"
LOADGEN_NAME="${LOADGEN_NAME:-checkout-loadgen}"
LOADGEN_IMAGE="${LOADGEN_IMAGE:-curlimages/curl:8.8.0}"
PHASE_SECONDS="${PHASE_SECONDS:-75}"
REQUEST_PERIOD_SECONDS="${REQUEST_PERIOD_SECONDS:-1}"
WORKER_SCHEDULE="${WORKER_SCHEDULE:-4,12,24,12,4,0,0}"
JITTER_SEED="${JITTER_SEED:-137}"
MAX_EXTRA_WORKERS="${MAX_EXTRA_WORKERS:-2}"

kubectl delete pod "${LOADGEN_NAME}" -n "${NAMESPACE}" --ignore-not-found >/dev/null 2>&1 || true

kubectl run "${LOADGEN_NAME}" \
  -n "${NAMESPACE}" \
  --image="${LOADGEN_IMAGE}" \
  --restart=Never \
  --env="SERVICE_URL=http://${SERVICE}.${NAMESPACE}.svc.cluster.local/" \
  --env="PHASE_SECONDS=${PHASE_SECONDS}" \
  --env="REQUEST_PERIOD_SECONDS=${REQUEST_PERIOD_SECONDS}" \
  --env="WORKER_SCHEDULE=${WORKER_SCHEDULE}" \
  --env="JITTER_SEED=${JITTER_SEED}" \
  --env="MAX_EXTRA_WORKERS=${MAX_EXTRA_WORKERS}" \
  --command -- sh -ceu '
    echo "starting pulsed load against ${SERVICE_URL}"
    phase_index=0
    rand_state="${JITTER_SEED}"
    next_rand() {
      rand_state=$(( (rand_state * 1103515245 + 12345) % 2147483648 ))
    }
    while true; do
      old_ifs="${IFS}"
      IFS=","
      set -- ${WORKER_SCHEDULE}
      IFS="${old_ifs}"
      for base_workers in "$@"; do
        phase_index=$(( phase_index + 1 ))
        next_rand
        worker_rand="${rand_state}"
        next_rand
        duration_rand="${rand_state}"
        extra_workers=$(( worker_rand % (MAX_EXTRA_WORKERS + 1) ))
        if [ $(( worker_rand % 5 )) -eq 0 ]; then
          extra_workers=0
        fi
        workers=$(( base_workers + extra_workers ))
        if [ "${base_workers}" -eq 0 ] && [ $(( worker_rand % 4 )) -ne 0 ]; then
          workers=0
        fi
        duration_offset=$(( (duration_rand % 21) - 10 ))
        duration=$(( PHASE_SECONDS + duration_offset ))
        if [ "${duration}" -lt 15 ]; then
          duration=15
        fi
        echo "phase=${phase_index} base_workers=${base_workers} workers=${workers} duration=${duration}s seed=${JITTER_SEED}"
        end=$(( $(date +%s) + duration ))
        if [ "${workers}" -le 0 ]; then
          sleep "${duration}"
          continue
        fi
        i=1
        while [ "${i}" -le "${workers}" ]; do
          (
            while [ "$(date +%s)" -lt "${end}" ]; do
              curl -fsS --max-time 4 "${SERVICE_URL}" >/dev/null || true
              sleep "${REQUEST_PERIOD_SECONDS}"
            done
          ) &
          i=$(( i + 1 ))
        done
        wait
      done
    done
  '

cat <<EOF
Started ${NAMESPACE}/${LOADGEN_NAME}.

Defaults create a pulsed pattern:
  WORKER_SCHEDULE=${WORKER_SCHEDULE}
  PHASE_SECONDS=${PHASE_SECONDS}
  REQUEST_PERIOD_SECONDS=${REQUEST_PERIOD_SECONDS}

Increase WORKER_SCHEDULE for stronger bursts, or delete the pod to stop traffic:
  kubectl -n ${NAMESPACE} delete pod ${LOADGEN_NAME}
EOF
