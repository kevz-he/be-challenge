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
  healthy, runs `test/seed --seed=42 --total=500 --ingest-url=http://api:8080/...`
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

## One-shot Python demo (`scripts/run_demo.py`)

Production-like end-to-end checker. **Stdlib only** (Python 3.9+, no
`pip install`). By default it drives **docker compose** exactly the way
a reviewer would:

```bash
python3 scripts/run_demo.py
# ALL GREEN — 22/22 checks passed
```

The default flow:

1. `docker compose down -v` (reset any prior volume).
2. `docker compose up -d --build api` — boots **only** the API service.
   The bundled `seeder` is intentionally skipped so this script owns the
   ingest and the oracle stays in sync.
3. Polls `/healthz` until the container's healthcheck reports healthy.
4. Bulk-ingests `testdata/transactions.json` via
   `POST /v1/transactions/batch` (in batches of `--batch-size`, default 1000).
5. Runs 22 assertions against `testdata/expected_counts.json`:
   - Counts of all four anomaly kinds (`/v1/health` + `/v1/anomalies` summary).
   - Health score within `1e-6` of the oracle.
   - ID-set equality for `orphaned`, `ghost`, `pending_limbo` (`items[]`)
     and `duplicate` (`groups[]`).
   - `/v1/alerts` returns the expected shape.
   - Error responses: `422` (inverted window) and `400` (invalid type).
   - Stretch breakdowns (`processor`, `payment_method`).
6. `docker compose down -v` (cleanup). Skip with `--keep`.

Exit code is `0` only when **all 22 checks pass**. On failure the
script tails `docker compose logs api` automatically.

### Other modes

```bash
python3 scripts/run_demo.py --keep                       # leave docker up after the run
python3 scripts/run_demo.py --mode local                 # spawn `go run ./cmd/server` instead
python3 scripts/run_demo.py --base http://localhost:8080 # external server, no boot/teardown
```

### Giant dataset (10 000 transactions)

The seed is parameterized; the same oracle scheme works at any size:

```bash
go run ./test/seed --seed=42 --total=10000 \
  --out=testdata/transactions.json \
  --expected=testdata/expected_counts.json

python3 scripts/run_demo.py            # picks up the new dataset automatically
```

Anomaly counts stay constant by construction (20 / 15 / 8 / 10); only
the healthy-pair denominator and `expected_health_score` move (e.g.
`0.7833` at 500 rows → `0.9886` at 10 000 rows). The script reads
both numbers from `testdata/expected_counts.json`, so no flag changes
are needed.

### Flags

| Flag              | Default                          | Purpose                                              |
| ----------------- | -------------------------------- | ---------------------------------------------------- |
| `--mode`          | `docker`                         | `docker` (production-like) or `local` (`go run`)     |
| `--base URL`      | unset                            | Point at an existing server (skips boot + teardown)  |
| `--keep`          | off                              | Do not tear down docker / local server after the run |
| `--tx-file PATH`  | `testdata/transactions.json`     | Override input dataset                               |
| `--expected PATH` | `testdata/expected_counts.json`  | Override oracle                                      |
| `--db-file PATH`  | `demo.db`                        | SQLite path when `--mode local`                      |
| `--batch-size N`  | `1000`                           | Rows per `POST /v1/transactions/batch` call          |

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
    "processor":      "ProcessorA",
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
window, 60/30/10 mix of credit_card/pix/boleto, 4 processors named
`ProcessorA`..`ProcessorD`, healthy-pair status mix approximating
processor 85/12/3 and merchant 87/10/3) and
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
| "End-to-end demo with a single command" | `python3 scripts/run_demo.py`           |
| "Full reset"                            | `make docker-reset`                     |

## Detailed design

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for:

- data model and schema
- exact time-window semantics
- SQL queries for each anomaly with explanation
- decisions (why `int64` cents, why `chargedback` is accepted but does
  not impact scores, why ingest is atomic, why `Clock` is injected)
- tradeoffs and what would change for Postgres / streaming / multi-tenant

## Roadmap & future improvements

The MVP intentionally optimizes for a reviewer-friendly demo (deterministic
seed, single SQLite file, sub-minute boot). The notes below outline the
work I would prioritize next, grouped by intent. They are deliberately
high-level; deeper plans live alongside the codebase as separate design
documents.

### Technical debt and polish

Tracked candidates, ordered roughly by effort vs. impact:

- **Tighten the HTTP/repository boundary.** `httpapi.Deps` currently takes a
  concrete `*sqlite.Repo` so `/healthz` can call `DB().Ping`. A small
  `Pinger` interface (or a `Health()` method on `repository.Repository`)
  would restore the layering documented in `CLAUDE.md`.
- **Sanitize error responses.** A few error paths still propagate raw
  `err.Error()` into `application/problem+json` `detail`. All user-facing
  details should be sanitized; internal context belongs only in logs.
- **Validate `breakdown` consistently.** `AnomaliesService` rejects unknown
  values with `400`; `HealthService.Compute` silently ignores them. Both
  services should share the same validation contract.
- **Honor `LOG_LEVEL`.** It is documented and set in `docker-compose.yml`,
  but `cmd/server` hardcodes `slog.LevelInfo`. Wiring it through `config`
  is a one-liner with high observability payoff.
- **Reduce duplicated detection work.** `/v1/health?breakdown=…` and the
  anomalies summary currently run the four detection queries twice (once
  for totals, once for the breakdown). A single pass that aggregates by key
  would roughly halve the cost of those endpoints.
- **Use `COUNT(*)` instead of `len(slice)` for counters.** Some count paths
  in `repository/sqlite` materialize full detail rows only to count them.
  Cheap to fix and meaningful past ~100k rows.
- **Tune SQLite PRAGMAs.** Add `busy_timeout`, `synchronous=NORMAL`, and a
  larger `cache_size` next to the existing WAL setting. Drop the dead
  `foreign_keys=ON` (no FKs in the schema).
- **Bound batch ingest at the service layer.** Today only an 8 MiB body
  cap protects the server. A row-count cap (e.g. 10 000) gives clearer
  errors and predictable latency.
- **Test hygiene.** Promote `t.Skipf` to `t.Fatalf` for fixtures committed
  to the repo (the oracle is mandatory, not optional), extract the shared
  `expectedOracle` / `repoRoot` helpers into `internal/testutil`, and add
  coverage for `livenessHandler`'s 503 path and `MaxBytesReader` overflow.
- **Move the in-memory `Fake` repository into its own subpackage** so
  production binaries don't link test-only code.
- **Logging granularity.** Differentiate log level by HTTP status (warn on
  4xx, error on 5xx) and sample `/healthz` to reduce noise in production.

### Scaling roadmap

The `Repository` interface is deliberately the only seam that touches the
database, which makes the storage swap below mechanical rather than
invasive. The roadmap is organized by sustained throughput tier:

| Tier    | Volume          | Sustained events/s | Strategy                                                   |
|---------|-----------------|--------------------|------------------------------------------------------------|
| MVP     | < 100k tx/day   | < 5/s              | current SQLite-backed service                              |
| Mid     | 1M tx/day       | ~24/s              | Postgres + idempotency + async ingest                      |
| Big     | 10M tx/day      | ~250/s             | + incremental detection and OLAP for reads                 |
| Extreme | 1M tx/min (1.4B/day) | ~33k/s sustained, 100k+/s peak | + stream processing, cell-based, multi-region |

**Wave 1 — Postgres without changing the architecture (up to ~200k/day).**
Swap the SQLite implementation for Postgres behind the existing
`Repository` interface. Range-partition by `occurred_at` (one partition per
day, drop after retention), add partial indexes for hot predicates
(`source='processor' AND status='approved'`, pending PIX/Boleto), enforce
client-supplied `event_id` with `INSERT … ON CONFLICT DO NOTHING`, and
split read/write pools (read replica for the analytics endpoints).

**Wave 2 — Idempotency + async ingest (up to ~1M/day).** Put Redis in
front of the writer for `SETNX event_id` idempotency, and Kafka (or
Kinesis) between the ingest API and a new `cmd/persist-worker` that
batches `COPY` into Postgres. The HTTP handler becomes a validator +
publisher; ingest p99 collapses from tens of milliseconds to single
digits. Add a DLQ with bounded retries and an alert on its depth.

**Wave 3 — Incremental detection + materialized anomalies.** A new
`cmd/detector-worker` consumes the same Kafka topic and maintains state
in Redis with a grace window per payment method. It writes into an
`anomalies` table (`detected_at`, `resolved_at`, `details JSONB`), which
is what `/v1/anomalies` reads. Aggregate counters in Redis serve
`/v1/health` and `/v1/alerts` in single-digit milliseconds, independent
of dataset size. A retroactive reconciliation job reopens/closes
anomalies when late events arrive.

**Wave 4 — Resilience and multi-tenant.** Outbox pattern for external
notifications, `tenant_id` propagated through the domain and used as part
of the Kafka and Postgres partitioning keys, Aurora multi-AZ with
automatic failover, HPA on the ingest API driven by CPU + Kafka lag, and
feature flags per anomaly type so a buggy detector can be disabled
without a redeploy.

**Wave 5 — Analytics and reporting (>10M/day).** Debezium → ClickHouse
(or Pinot/Snowflake) for historical reporting and trend dashboards
without touching the OLTP path. Optional statistical anomaly detection on
top of the rule-based engine.

**Wave 6 — Extreme scale (1M tx/min).** At this tier the assumptions of
the earlier waves break and the architecture changes shape:

- **Event log:** Cassandra/ScyllaDB. Postgres demotes to "materialized
  outputs + tenant metadata" only.
- **Stream processing:** Apache Flink (RocksDB state, exactly-once,
  windowed co-joins for orphaned/ghost, timer service for pending limbo).
  A Go worker with Redis state stops scaling around tens of thousands of
  events per second.
- **OLAP serving:** Apache Pinot or Druid for sub-100ms `/v1/health` and
  `/v1/anomalies` over billions of rows, with segment pruning by time.
- **Kafka:** dedicated cluster per cell, tiered storage to S3,
  Avro/Protobuf payloads via Schema Registry to cut wire size 5–10×.
- **Cell-based architecture:** each cell is a self-contained stack
  (Kafka + Flink + Scylla + Pinot + Postgres + Redis) serving a tenant
  subset. Bounded blast radius and per-cell canaries.
- **Multi-region active-active:** MirrorMaker2, Cassandra multi-DC,
  CRDT-based global counters, anycast/DNS-based geo routing.
- **Edge ingest:** Cloudflare Workers / API Gateway handling auth, rate
  limiting and regional dedupe before the central pipeline.
- **Cost & resilience:** Zstd everywhere, hot/warm/cold tiering,
  per-tenant cost dashboards, formal SLOs with error budgets, chaos
  engineering, shadow traffic, game days.

### Cross-cutting work that pays off at every tier

- **Observability:** Prometheus metrics (ingested tx/s, Kafka lag, detector
  p50/p99, anomalies/min, HTTP latency by route) and OpenTelemetry tracing
  with `trace_id` propagated through Kafka headers end-to-end.
- **Resilience patterns:** end-to-end backpressure (429 before saturation),
  bulkheads per tenant tier, replay from Kafka after a hot-fix, monitoring
  of `ingested_at − occurred_at` for clock drift.
- **Schema discipline:** binary payloads (Avro/Protobuf) with Schema
  Registry compatibility checks once more than one producer or consumer
  ships independently.

### ROI summary (highest impact first)

1. Idempotency + async ingest via Redis + Kafka (50 → 1 000 tx/s, low
   effort).
2. Incremental detection with a materialized `anomalies` table (constant
   read latency, medium effort).
3. Postgres partitioning + read replicas (covers 10M/day with low
   incremental effort).
4. Flink + Cassandra (only justified by the 1M tx/min target; high
   effort).
5. Cell-based architecture and multi-region active-active (blast radius
   and DR; only for strong SLAs).

## Layout

```
cmd/server/        # API entrypoint
cmd/healthcheck/   # tiny binary used by the Docker healthcheck
test/seed/         # deterministic test-data generator + oracle (not a prod cmd)
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
