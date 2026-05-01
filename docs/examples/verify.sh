#!/usr/bin/env bash
# verify.sh — acceptance script for reviewers.
#
# Drives the live server with curl and jq and compares each response against
# testdata/expected_counts.json. Fails (exit 1) on the first mismatch, prints
# OK lines otherwise. Idempotent: safe to re-run.
#
# Defaults to http://localhost:8080. Override with `BASE=http://host:port verify.sh`.
#
# Tested counts:
#   - GET /v1/health.anomaly_counts.{orphaned,ghost,duplicate,pending_limbo}
#   - GET /v1/anomalies?type=...&limit=1000 → length of items[]
#
# Tested ID sets (set equality, sorted diff via comm):
#   - orphaned, ghost, pending_limbo  → items[].transaction_id
#   - duplicate                       → groups[].transaction_id (deduped)
#
# Tested health score: matches expected_health_score within 1e-6.

set -euo pipefail

BASE=${BASE:-http://localhost:8080}
EXPECTED=${EXPECTED:-testdata/expected_counts.json}

if ! command -v jq >/dev/null 2>&1; then
  echo "verify.sh requires jq. Install it (brew install jq / apt install jq) and retry." >&2
  exit 2
fi

if [[ ! -f "${EXPECTED}" ]]; then
  echo "expected file not found: ${EXPECTED}" >&2
  exit 2
fi

# Read window from oracle for explicit query.
FROM=$(jq -r '.window_from' "${EXPECTED}")
TO=$(jq -r '.window_to' "${EXPECTED}")
HQ="${BASE}/v1/health?from=${FROM}&to=${TO}"

pass=0
fail=0
ok()   { printf "OK   %s\n" "$*"; pass=$((pass+1)); }
bad()  { printf "FAIL %s\n" "$*" >&2; fail=$((fail+1)); }

check_count() {
  local label=$1 url=$2 jq_got=$3 jq_want=$4
  local got want
  got=$(curl -fsSL "${url}" | jq -r "${jq_got}")
  want=$(jq -r "${jq_want}" "${EXPECTED}")
  if [[ "${got}" == "${want}" ]]; then
    ok "${label}: ${got}"
  else
    bad "${label}: got=${got} want=${want} (url=${url})"
  fi
}

check_set() {
  local label=$1 url=$2 jq_got=$3 jq_want=$4
  local tmp_got tmp_want
  tmp_got=$(mktemp)
  tmp_want=$(mktemp)
  curl -fsSL "${url}" | jq -r "${jq_got}" | sort -u > "${tmp_got}"
  jq -r "${jq_want}" "${EXPECTED}" | sort -u > "${tmp_want}"
  if diff -q "${tmp_got}" "${tmp_want}" >/dev/null; then
    ok "${label}: $(wc -l < "${tmp_got}" | tr -d ' ') IDs match"
  else
    bad "${label}: ID set mismatch"
    echo "  --- expected (want)" >&2
    diff "${tmp_want}" "${tmp_got}" >&2 || true
  fi
  rm -f "${tmp_got}" "${tmp_want}"
}

check_score() {
  local got want match
  got=$(curl -fsSL "${HQ}" | jq -r '.score')
  want=$(jq -r '.expected_health_score // .health_score' "${EXPECTED}")
  match=$(awk -v g="${got}" -v w="${want}" 'BEGIN { d=g-w; if (d<0) d=-d; print (d<1e-6) ? "yes" : "no" }')
  if [[ "${match}" == "yes" ]]; then
    ok "health score: ${got} (~ ${want})"
  else
    bad "health score: got=${got} want=${want}"
  fi
}

echo "verify.sh BASE=${BASE} EXPECTED=${EXPECTED}"

check_count "orphaned (health)"      "${HQ}" '.anomaly_counts.orphaned'      '.anomaly_counts.orphaned'
check_count "ghost (health)"         "${HQ}" '.anomaly_counts.ghost'         '.anomaly_counts.ghost'
check_count "duplicate (health)"     "${HQ}" '.anomaly_counts.duplicate'     '.anomaly_counts.duplicate'
check_count "pending_limbo (health)" "${HQ}" '.anomaly_counts.pending_limbo' '.anomaly_counts.pending_limbo'

check_count "items orphaned"      "${BASE}/v1/anomalies?from=${FROM}&to=${TO}&type=orphaned&limit=1000"      '.items | length' '.anomaly_counts.orphaned'
check_count "items ghost"         "${BASE}/v1/anomalies?from=${FROM}&to=${TO}&type=ghost&limit=1000"         '.items | length' '.anomaly_counts.ghost'
check_count "items pending_limbo" "${BASE}/v1/anomalies?from=${FROM}&to=${TO}&type=pending_limbo&limit=1000" '.items | length' '.anomaly_counts.pending_limbo'

check_set "ID set orphaned"       "${BASE}/v1/anomalies?from=${FROM}&to=${TO}&type=orphaned&limit=1000"      '.items[].transaction_id'        '.ids.orphaned[]'
check_set "ID set ghost"          "${BASE}/v1/anomalies?from=${FROM}&to=${TO}&type=ghost&limit=1000"         '.items[].transaction_id'        '.ids.ghost[]'
check_set "ID set pending_limbo"  "${BASE}/v1/anomalies?from=${FROM}&to=${TO}&type=pending_limbo&limit=1000" '.items[].transaction_id'        '.ids.pending_limbo[]'
check_set "ID set duplicate"      "${BASE}/v1/anomalies?from=${FROM}&to=${TO}&type=duplicate&limit=1000"     '.groups | map(.transaction_id) | unique | .[]' '.ids.duplicate[]'

check_score

echo
echo "PASSED=${pass} FAILED=${fail}"
[[ "${fail}" -eq 0 ]]
