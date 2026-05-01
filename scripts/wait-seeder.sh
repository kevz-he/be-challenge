#!/usr/bin/env bash
# Block until the seed container in docker-compose has finished running
# its one-shot ingest and exited successfully. Used by `make acceptance`
# between `wait-healthy` and `verify` to eliminate the race where
# `verify.sh` runs against a partially-ingested API.
#
# Strategy:
#   - Poll `docker inspect` for the container State.Status until it is
#     either `exited` (great) or we hit the timeout.
#   - When it has exited, read `State.ExitCode` and propagate it.
#   - We deliberately avoid `docker wait` + GNU `timeout` because the
#     coreutils `timeout` is not available on macOS by default and the
#     acceptance flow needs to work portably for reviewers.

set -euo pipefail

CONTAINER=${CONTAINER:-yuno-seeder}
TIMEOUT=${TIMEOUT:-120}
INTERVAL=${INTERVAL:-1}
LOG_TAIL=${LOG_TAIL:-100}

dump_diagnostics() {
  echo "--- last ${LOG_TAIL} lines from ${CONTAINER} ---" >&2
  docker logs "${CONTAINER}" --tail "${LOG_TAIL}" >&2 2>&1 || echo "(could not read logs for ${CONTAINER})" >&2
  echo "--- inspected state ---" >&2
  docker inspect --format='{{json .State}}' "${CONTAINER}" >&2 2>/dev/null || true
}

elapsed=0
while [[ "${elapsed}" -lt "${TIMEOUT}" ]]; do
  if ! docker inspect "${CONTAINER}" >/dev/null 2>&1; then
    sleep "${INTERVAL}"
    elapsed=$((elapsed + INTERVAL))
    continue
  fi

  status=$(docker inspect --format='{{.State.Status}}' "${CONTAINER}" 2>/dev/null || echo "missing")
  case "${status}" in
    exited)
      exit_code=$(docker inspect --format='{{.State.ExitCode}}' "${CONTAINER}" 2>/dev/null || echo "127")
      if [[ "${exit_code}" == "0" ]]; then
        echo "${CONTAINER} finished cleanly (exit=0)"
        exit 0
      fi
      echo "${CONTAINER} exited with code ${exit_code}" >&2
      dump_diagnostics
      exit "${exit_code}"
      ;;
    dead)
      echo "${CONTAINER} entered dead state" >&2
      dump_diagnostics
      exit 1
      ;;
    created|running|restarting|paused|removing|missing)
      printf "."
      ;;
    *)
      printf "?"
      ;;
  esac
  sleep "${INTERVAL}"
  elapsed=$((elapsed + INTERVAL))
done

echo "timeout waiting for ${CONTAINER} to exit (after ${TIMEOUT}s)" >&2
dump_diagnostics
exit 1
