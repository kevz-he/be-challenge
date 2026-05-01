#!/usr/bin/env bash
# End-to-end demo for the Yuno Transaction Health Monitor.
#
# Hits every endpoint against a server that has already been seeded
# (e.g. by `docker compose up`, which runs test/seed via the seeder
# service). It is intentionally read-only on the oracle window so it
# can be re-run any number of times without invalidating the counts
# asserted by `verify.sh` and the committed `expected_counts.json`.
#
# Order:
#   1. liveness + analytical reads against the seeded dataset
#   2. expected error responses (422 / 400)
#   3. an ingest example that lives OUTSIDE the oracle window AND is
#      paired with a merchant_order_system row, so it cannot become
#      orphaned/ghost no matter how the window is widened.
#
# Usage:
#   bash docs/examples/curl.sh
#   BASE=http://localhost:8080 bash docs/examples/curl.sh
#
# Set INGEST_BATCH=1 if you want to re-ingest testdata/transactions.json
# (only useful against an empty database; against a seeded one it would
# duplicate every row and break the oracle).

set -euo pipefail

BASE="${BASE:-http://localhost:8080}"
WINDOW_FROM="${WINDOW_FROM:-2026-04-15T00:00:00Z}"
WINDOW_TO="${WINDOW_TO:-2026-04-15T06:00:00Z}"
TX_FILE="${TX_FILE:-testdata/transactions.json}"
INGEST_BATCH="${INGEST_BATCH:-0}"

# Demo single-ingest is anchored well before the oracle window so it
# never participates in any windowed query, and it is paired with a
# merchant row so a wider window would still produce a healthy pair.
DEMO_TS="${DEMO_TS:-2026-04-14T22:00:00Z}"
DEMO_ID="${DEMO_ID:-demo-single-001}"

JQ=$(command -v jq || true)

print_header() { printf "\n=== %s ===\n" "$1"; }

print_header "liveness /healthz"
curl -fsS "$BASE/healthz"
echo

print_header "GET /v1/health (window)"
curl -fsS "$BASE/v1/health?from=$WINDOW_FROM&to=$WINDOW_TO"
echo

print_header "GET /v1/health (breakdown=processor)"
curl -fsS "$BASE/v1/health?from=$WINDOW_FROM&to=$WINDOW_TO&breakdown=processor"
echo

print_header "GET /v1/health (breakdown=payment_method)"
curl -fsS "$BASE/v1/health?from=$WINDOW_FROM&to=$WINDOW_TO&breakdown=payment_method"
echo

print_header "GET /v1/anomalies (summary)"
curl -fsS "$BASE/v1/anomalies?from=$WINDOW_FROM&to=$WINDOW_TO"
echo

for kind in orphaned ghost duplicate pending_limbo; do
  print_header "GET /v1/anomalies?type=$kind&limit=3"
  curl -fsS "$BASE/v1/anomalies?from=$WINDOW_FROM&to=$WINDOW_TO&type=$kind&limit=3"
  echo
done

print_header "GET /v1/alerts"
curl -fsS "$BASE/v1/alerts?from=$WINDOW_FROM&to=$WINDOW_TO"
echo

print_header "errors: invalid window (422)"
curl -sS -o /dev/null -w "status=%{http_code}\n" \
  "$BASE/v1/health?from=$WINDOW_TO&to=$WINDOW_FROM"

print_header "errors: invalid type (400)"
curl -sS -o /dev/null -w "status=%{http_code}\n" \
  "$BASE/v1/anomalies?type=nope"

print_header "errors: unsupported media type (415)"
curl -sS -o /dev/null -w "status=%{http_code}\n" \
  -X POST -H 'Content-Type: text/plain' \
  --data-binary '{"transaction_id":"x"}' \
  "$BASE/v1/transactions"

# ---- ingest examples (last, on data that does not affect the oracle) ----

print_header "ingest single transaction (outside oracle window, paired)"
curl -fsS -X POST -H 'Content-Type: application/json' \
  -d "$(cat <<EOF
{
  "transaction_id": "${DEMO_ID}",
  "occurred_at":    "${DEMO_TS}",
  "amount_cents":   1234,
  "currency":       "BRL",
  "payment_method": "pix",
  "processor":      "stripe_br",
  "status":         "approved",
  "source":         "processor"
}
EOF
)" \
  "$BASE/v1/transactions"
echo

curl -fsS -X POST -H 'Content-Type: application/json' \
  -d "$(cat <<EOF
{
  "transaction_id": "${DEMO_ID}",
  "occurred_at":    "${DEMO_TS}",
  "amount_cents":   1234,
  "currency":       "BRL",
  "payment_method": "pix",
  "processor":      "stripe_br",
  "status":         "approved",
  "source":         "merchant_order_system"
}
EOF
)" \
  "$BASE/v1/transactions"
echo

if [[ "$INGEST_BATCH" == "1" && -f "$TX_FILE" ]]; then
  print_header "ingest seed batch ($TX_FILE) — INGEST_BATCH=1"
  echo "WARNING: this duplicates the seeded dataset and will invalidate the oracle." >&2
  if [[ -n "$JQ" ]]; then
    BODY=$(jq '{transactions: .}' "$TX_FILE")
  else
    BODY=$(printf '{"transactions": %s}' "$(cat "$TX_FILE")")
  fi
  printf '%s' "$BODY" | curl -fsS -X POST -H 'Content-Type: application/json' \
    --data-binary @- "$BASE/v1/transactions/batch" | head -c 200
  echo
fi
