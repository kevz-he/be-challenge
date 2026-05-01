# Yuno Transaction Health Monitor

Go HTTP service that ingests transactions from processors and from the
merchant order system, detects four kinds of reconciliation anomalies,
exposes a health score filterable by time window, and triggers alerts
when configurable thresholds are crossed.

- **Stack:** Go 1.22+ · `chi` · SQLite (via `modernc.org/sqlite`, no CGO).
- **Layers:** `domain` <- `service` <- `repository` (interface + SQLite impl) <- `httpapi`.
- **Money:** `int64` cents. Currency is always `BRL`.
- **Clock:** `clock.Clock` interface with `Real` and `Fake` (tests never call `time.Now()` directly).

## Quickstart with Docker (≈ 30 seconds, the recommended path)

```bash
docker compose up --build      # boots API + auto-seeds it
bash docs/examples/verify.sh   # diff every count and ID set vs the oracle
```

`docker compose up` starts two services:

- `api` — distroless static image (`gcr.io/distroless/static-debian12:nonroot`),
  exposes `:8080`, persists SQLite (WAL) under the `yuno-data` volume,
  reports health via the `cmd/healthcheck` Go binary baked into the image.
- `seeder` — same Docker context, `target=seeder`. Waits for the API to be
  healthy, runs `cmd/seed --seed=42 --total=500 --ingest-url=http://api:8080/...`
  exactly once, then writes `/data/.seeded` so re-runs are no-ops. Reset with:

```bash
make docker-reset    # = docker compose down -v
```

Then any of the following works against the running API:

```bash
curl -s http://localhost:8080/v1/health | jq
curl -s 'http://localhost:8080/v1/anomalies?type=orphaned' | jq
make verify           # docs/examples/verify.sh — fails if any count or ID differs
make demo-curls       # docs/examples/curl.sh — exercises every endpoint
```

To run the full Go test suite inside Docker (no local Go required):

```bash
make qa-docker        # docker build -f Dockerfile.test --target test .
```

## Quickstart with local Go (alternative)

```bash
make seed             # writes testdata/transactions.json + expected_counts.json
make run              # listens on :8080, uses ./yuno.db
# in another terminal:
make demo-curls       # ingests + hits every endpoint
```

`make demo-curls` runs `docs/examples/curl.sh`, which is read-only on
the oracle window so it can be re-run safely against an already seeded
service. It performs, in order:

1. `GET /healthz`
2. `GET /v1/health` (with and without breakdowns)
3. `GET /v1/anomalies` (summary + per type with `?type=`)
4. `GET /v1/alerts`
5. Expected errors (invalid window → 422, invalid type → 400, wrong content-type → 415)
6. `POST /v1/transactions` — a paired processor + merchant pair anchored
   **outside** the oracle window, so the demo never alters the counts
   asserted by `verify.sh`.

`POST /v1/transactions/batch` is intentionally **not** exercised by the
default demo: when the API is already seeded (e.g. via `docker compose
up`), re-ingesting the seed batch would duplicate every row and break
the oracle. Set `INGEST_BATCH=1` to opt-in against an empty database.

## Environment variables

| Variable                   | Default                                            | Description                                  |
| -------------------------- | -------------------------------------------------- | -------------------------------------------- |
| `PORT`                     | `8080`                                             | HTTP port                                    |
| `SQLITE_DSN`               | `file:yuno.db?_pragma=journal_mode(WAL)`           | SQLite DSN (`:memory:` for tests)            |
| `PIX_PENDING_THRESHOLD`    | `24h`                                              | Age at which a pending pix becomes limbo     |
| `BOLETO_PENDING_THRESHOLD` | `72h`                                              | Same idea for boleto                         |
| `ALERT_ORPHANED_THRESHOLD` | `50`                                               | Triggers an alert if orphaned exceeds it     |
| `ALERT_GHOST_THRESHOLD`    | `100`                                              | Triggers an alert if ghost exceeds it        |
| `ALERT_HEALTH_MIN`         | `0.95`                                             | Triggers an alert if score < min             |
| `SERVER_READ_TIMEOUT`      | `15s`                                              | `http.Server.ReadTimeout`                    |
| `SERVER_WRITE_TIMEOUT`     | `30s`                                              | `http.Server.WriteTimeout`                   |
| `SERVER_IDLE_TIMEOUT`      | `60s`                                              | `http.Server.IdleTimeout`                    |
| `SHUTDOWN_TIMEOUT`         | `15s`                                              | Maximum graceful-shutdown duration           |

## Endpoints

All responses use `application/json`; errors use `application/problem+json` (RFC 7807).

### `POST /v1/transactions`

Ingests a transaction. `201 Created` or `400` with detail.

```bash
curl -X POST http://localhost:8080/v1/transactions \
  -H 'Content-Type: application/json' \
  -d '{
    "transaction_id": "tx-1",
    "occurred_at":    "2026-04-15T01:23:45Z",
    "amount_cents":   1234,
    "currency":       "BRL",
    "payment_method": "pix",
    "processor":      "stripe_br",
    "status":         "approved",
    "source":         "processor"
  }'
```

### `POST /v1/transactions/batch`

**Atomic** ingest: if a single transaction is invalid, the whole batch is
rejected and nothing is persisted. The error response includes `errors[]`
with index and reason for each invalid row.

```json
{ "transactions": [ { ... }, { ... } ] }
```

### `GET /v1/health?from=&to=&breakdown=`

Health score between 0 and 1. `from`/`to` in RFC3339 are optional. Without
a window the entire dataset is analyzed. `breakdown` in `processor | payment_method`.

```json
{
  "score": 0.7833,
  "window": { "from": "...", "to": "..." },
  "unique_transactions": 263,
  "total_rows": 500,
  "anomaly_counts": {
    "orphaned": 20, "ghost": 15, "duplicate": 8, "pending_limbo": 10
  },
  "duplicate_extra_rows": 12
}
```

### `GET /v1/anomalies?from=&to=&type=&limit=&offset=`

Without `type` -> summary + sample (5) per type. With `type` in `orphaned|ghost|duplicate|pending_limbo` returns a paginated detail.
For `duplicate`, `groups[]` is returned (each group carries all of its rows) instead of `items[]`.

### `GET /v1/alerts?from=&to=`

Evaluates the thresholds (`ALERT_*`) against the window's `health`. Response:

```json
{ "alert": true, "triggered": [ { "rule": "...", "value": 75, "threshold": 50, "message": "..." } ] }
```

### `GET /healthz`

Liveness; if the DB answers `Ping` it returns `{"status": "ok"}`.

## Test data and oracle

`make seed` generates `testdata/transactions.json` (>=500 transactions, 6h
window, 60/30/10 mix of credit_card/pix/boleto, 4 processors) and
`testdata/expected_counts.json`. The oracle is **set-equality strong**:
it commits both the counts and the exact `transaction_id`s injected per
anomaly type, so tests can assert "exactly these IDs and no others":

```json
{
  "window_from": "2026-04-15T00:00:00Z",
  "window_to":   "2026-04-15T06:00:00Z",
  "now":         "2026-04-22T00:00:00Z",
  "total_rows":  500,
  "unique_transaction_ids_in_window": 263,
  "anomaly_counts": {
    "orphaned": 20, "ghost": 15, "duplicate": 8, "pending_limbo": 10
  },
  "duplicate_extra_rows": 12,
  "health_score": 0.7833,
  "expected_health_score": 0.7833,
  "ids": {
    "orphaned":      ["ORPHAN-000", "ORPHAN-001", "..."],
    "ghost":         ["GHOST-NOPROC-000", "GHOST-BADPROC-000", "..."],
    "duplicate":     ["DUP-PROC-000", "DUP-MERCH-000", "..."],
    "pending_limbo": ["LIMBO-000", "LIMBO-001", "..."]
  }
}
```

The generator injects **exactly** 20 orphaned, 15 ghost, 8 duplicate
groups and 10 pending limbo (5 pix > 24h, 5 boleto > 72h). The committed
oracle is the ground truth used by the SQLite integration tests, the
HTTP golden tests, the e2e test, and `verify.sh`.

## Tests

```bash
make test          # unit + integration, race detector on
make test-cover    # writes coverage.out and prints `go tool cover -func` summary
make test-e2e      # build tag e2e, full HTTP flow against the seed dataset
make qa            # fmt-check + vet + test + test-e2e (full local QA gate)
make qa-docker     # the same suite inside Docker (no local Go required)
```

Coverage targets (rule of thumb, not enforced):

| Package                          | Target |
|----------------------------------|--------|
| `internal/service`               | ≥ 90%  |
| `internal/repository/sqlite`     | ≥ 80%  |
| `internal/httpapi`               | ≥ 75%  |
| Global                           | ≥ 80%  |

Golden response snapshots live in `testdata/golden/*.json`. Regenerate them with:

```bash
go test ./internal/httpapi -run TestHTTP_GoldenResponses -update
```

## Verifying every Core Requirement

| Core Requirement                        | Command                                 |
|-----------------------------------------|-----------------------------------------|
| "Ingest test data"                      | `docker compose up` (auto-seeds 500+ rows) |
| "Query health metrics"                  | `curl /v1/health`                       |
| "Query anomaly details"                 | `curl /v1/anomalies?type=orphaned` etc. |
| "Filter by time window"                 | `?from=…&to=…` on every analytic route  |
| "Counts + IDs match the oracle"         | `make verify` (fails on any mismatch)   |
| "Full reset"                            | `make docker-reset`                     |

## Detailed design

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for:

- data model and schema
- exact time-window semantics
- SQL queries for each anomaly with explanation
- decisions (why `int64` cents, why `chargedback` is accepted but does
  not impact scores, why ingest is atomic, why `Clock` is injected)
- tradeoffs and what would change for Postgres / streaming / multi-tenant

## Layout

```
cmd/server/        # API entrypoint
cmd/seed/          # deterministic generator + oracle
internal/domain/   # pure types + validation + errors
internal/clock/    # Clock interface (Real + Fake)
internal/config/
internal/repository/         # interface + Window + AnomalyFilter + in-memory fake
internal/repository/sqlite/  # SQLite impl + embedded schema.sql
internal/service/  # ingest, anomalies, health, alerts
internal/httpapi/  # chi router + handlers + DTOs + problem+json
testdata/          # transactions.json + expected_counts.json (committed)
docs/              # ARCHITECTURE.md + examples/curl.sh
```
