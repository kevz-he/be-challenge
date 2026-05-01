#!/usr/bin/env bash
# Block until the API container in docker-compose reports `healthy`, or until
# the timeout expires. Used by `make acceptance` between `up` and `verify`.
set -euo pipefail

CONTAINER=${CONTAINER:-yuno-api}
TIMEOUT=${TIMEOUT:-60}
INTERVAL=${INTERVAL:-1}
LOG_TAIL=${LOG_TAIL:-50}

dump_diagnostics() {
  echo "--- last ${LOG_TAIL} lines from ${CONTAINER} ---" >&2
  docker logs "${CONTAINER}" --tail "${LOG_TAIL}" >&2 2>&1 || echo "(could not read logs for ${CONTAINER})" >&2
  echo "--- last healthcheck output ---" >&2
  docker inspect --format='{{json .State.Health}}' "${CONTAINER}" >&2 2>/dev/null || true
}

elapsed=0
while [[ "${elapsed}" -lt "${TIMEOUT}" ]]; do
  status=$(docker inspect --format='{{.State.Health.Status}}' "${CONTAINER}" 2>/dev/null || echo "missing")
  case "${status}" in
    healthy) echo "${CONTAINER} is healthy"; exit 0 ;;
    unhealthy)
      echo "${CONTAINER} is unhealthy" >&2
      dump_diagnostics
      exit 1
      ;;
    missing) echo "${CONTAINER} not found yet, retrying..." ;;
    *) printf "."; ;;
  esac
  sleep "${INTERVAL}"
  elapsed=$((elapsed + INTERVAL))
done

echo "timeout waiting for ${CONTAINER}" >&2
dump_diagnostics
exit 1
