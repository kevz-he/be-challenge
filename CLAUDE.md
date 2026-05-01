# CLAUDE.md

This file is the source of truth for any AI agent (or human) working on this repository. It consolidates every design decision taken so far for the **Yuno Transaction Health Monitor** challenge. Read it before making changes.

---

## 1. Project goal

Build a backend HTTP service that:

1. Ingests payment transaction records from two sources (`processor`, `merchant_order_system`).
2. Detects four reconciliation anomalies: orphaned processor approvals, ghost orders, duplicate submissions, and pending limbo (PIX/Boleto stuck).
3. Exposes endpoints for health metrics, anomaly details, optional breakdowns (processor / payment method), and an alerting endpoint.
4. Supports time-window filtering on every analytics endpoint.

The service must be reproducible in under a minute by a reviewer (`docker compose up`) and demonstrably correct against a deterministic test dataset whose anomaly IDs are committed as ground truth.

---

## 2. Tech stack

| Concern | Choice | Why |
|---|---|---|
| Language | Go 1.22+ | New `net/http` routing, `log/slog` in stdlib, fast iteration. |
| HTTP router | `github.com/go-chi/chi/v5` | Idiomatic middleware (RequestID, Logger, Recoverer, Timeout). |
| Storage | `modernc.org/sqlite` | Pure-Go SQLite, no CGO, fits 340k+ rows, trivial `:memory:` tests. |
| Migrations | Single embedded `schema.sql` + idempotent `Migrate(ctx)` | No migration runner needed for MVP. |
| Logging | `log/slog` (JSON) | Stdlib, structured, no extra dependency. |
| Config | Env vars with defaults | Simple, 12-factor friendly. |
| Tests | stdlib `testing` + `httptest` + `:memory:` SQLite | Race detector enabled (`-race`). Optional `golangci-lint`. |
| Container | Multi-stage build → `gcr.io/distroless/static-debian12:nonroot` | Static binary (`CGO_ENABLED=0`), minimal attack surface. |

**Dependencies are intentionally minimal.** Only `chi` and `modernc.org/sqlite` in runtime. Anything else must be justified.

---

## 3. Architecture (hexagonal-lite, layered)

```
domain  ←  service  ←  repository (interface + sqlite impl)  ←  httpapi
                           ↑
                       cmd/server wires everything
```

- `internal/domain` — pure types, enums, validation, typed errors. No imports from other internal packages.
- `internal/clock` — `Clock` interface with `realClock` and `fakeClock`. **Pending limbo never calls `time.Now()` directly.**
- `internal/config` — env-var loading with defaults.
- `internal/repository` — `Repository` interface + `Window`, `AnomalyFilter`. Fake in-memory impl for service unit tests.
- `internal/repository/sqlite` — concrete impl, owns SQL and indexes. Embeds `schema.sql`.
- `internal/service` — business logic; depends on `Repository` interface only. Files: `ingest.go`, `anomalies.go`, `health.go`, `alerts.go`.
- `internal/httpapi` — chi server, handlers, DTOs, error rendering.
- `cmd/server` — entrypoint, wiring, graceful shutdown.
- `cmd/seed` — deterministic test-data generator + ground-truth oracle writer.
- `cmd/healthcheck` — tiny binary used by Docker healthcheck (distroless has no shell).

### Project layout

```
yuno/
├── cmd/{server,seed,healthcheck}/main.go
├── internal/
│   ├── domain/
│   ├── clock/
│   ├── config/
│   ├── repository/{repository.go, sqlite/{sqlite.go, transactions.go, schema.sql}}
│   ├── service/{ingest,anomalies,health,alerts}.go
│   └── httpapi/{server,handlers_*,dto,errors}.go
├── testdata/
│   ├── transactions.json          # seed output, committed
│   ├── expected_counts.json       # oracle: counts + IDs + expected_health_score
│   └── golden/*.json              # HTTP response snapshots
├── e2e/e2e_test.go                # build tag: e2e
├── docs/{ARCHITECTURE.md, examples/{curl.sh, verify.sh}}
├── Dockerfile, Dockerfile.test, .dockerignore, docker-compose.yml
├── Makefile, go.mod, README.md
```

---

## 4. Data model

Single table `transactions`, one row per ingested event (no upsert by `transaction_id`):

| Column | Type | Notes |
|---|---|---|
| `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` | Surrogate key. |
| `transaction_id` | `TEXT NOT NULL` | NOT unique on its own — duplicates are detection signal. |
| `occurred_at` | `DATETIME NOT NULL` | Event timestamp. |
| `ingested_at` | `DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP` | Server-side. |
| `amount_cents` | `INTEGER NOT NULL CHECK (amount_cents > 0)` | **Money is always int64 cents. Never floats.** |
| `currency` | `TEXT NOT NULL` | Domain-validated to `"BRL"`. |
| `payment_method` | `TEXT` CHECK in (`credit_card`,`pix`,`boleto`) | |
| `processor` | `TEXT NOT NULL` | Free-form (ProcessorA..D in seed). |
| `status` | `TEXT` CHECK in (`approved`,`declined`,`pending`,`failed`,`refunded`,`chargedback`) | `chargedback` is accepted but does **not** affect any anomaly in the MVP (documented). |
| `source` | `TEXT` CHECK in (`processor`,`merchant_order_system`) | |

Indexes: `(transaction_id)`, `(transaction_id, source)`, `(occurred_at)`, `(source, status)`, `(processor)`, `(payment_method)`.

---

## 5. HTTP API

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/transactions` | Ingest a single transaction. |
| `POST` | `/v1/transactions/batch` | Ingest N. **Atomic**: if one fails validation, the entire batch is rejected; response includes `errors[]` with offending index. |
| `GET` | `/v1/health?from=&to=&breakdown=processor\|payment_method` | Score + counts, optional segmentation. |
| `GET` | `/v1/anomalies?from=&to=&type=orphaned\|ghost\|duplicate\|pending_limbo&breakdown=&limit=&offset=` | Without `type` returns a summary plus a sample of 5 per type. |
| `GET` | `/v1/alerts?from=&to=` | Threshold evaluation, returns `{alert: bool, triggered:[{rule,value,threshold,message}]}`. |
| `GET` | `/healthz` | Liveness only. Unrelated to the health score. |

- `from`/`to` are RFC3339. `from > to` → `400`. Negative `limit`/`offset` → `400`.
- Error responses use `application/problem+json` (RFC 7807) with `type`, `title`, `status`, `detail`.

---

## 6. Time-window semantics (CRITICAL — read carefully)

The window is applied **only to the primary side of each anomaly**. The counterpart lookup spans the entire dataset. This avoids false positives at boundaries (e.g. processor at 23:59 vs merchant at 00:01).

- **Orphaned**: processor `approved` rows with `occurred_at ∈ [from, to]`. Counterpart in merchant searched across the **whole** dataset.
- **Ghost**: merchant `approved` rows with `occurred_at ∈ [from, to]`. Counterpart in processor across the whole dataset; ghost if missing OR if the **most recent** processor status (`MAX(occurred_at)`) is `declined`/`failed`.
- **Duplicate**: groups with the same `(transaction_id, source)` where **at least one row** in the group falls in `[from, to]`. The full group is returned in details.
- **Pending limbo**: processor `pending` rows in `[from, to]` AND `now − occurred_at > threshold`, where `now` comes from the injected `Clock` and `threshold` is per payment method (24h PIX, 72h Boleto). `>` strict (the `==` boundary does not count; documented).

If `from`/`to` are absent, the whole dataset is analyzed.

Anomalies are **not mutually exclusive**: a transaction may appear in multiple categories. This is documented behavior, not a bug.

---

## 7. Health score formula

```
duplicate_extra_records = total_duplicate_rows − duplicate_groups
anomalies_total         = orphaned + ghost + duplicate_extra_records + pending_limbo
denominator             = COUNT(DISTINCT transaction_id within window)
score                   = clamp(1 − anomalies_total / denominator, 0, 1)
```

- Empty dataset (`denominator == 0`) → `score = 1.0`. Documented as "empty == healthy".
- `duplicate_groups` is what is shown in `anomaly_counts.duplicate`.
- `duplicate_extra_records` is what affects the score (so a group of 3 contributes 2, not 3).
- The committed oracle includes `expected_health_score` recalculated with this formula for the seed dataset.

---

## 8. Anomaly detection (queries, in plain SQL)

### Orphaned

```sql
SELECT p.transaction_id, p.processor, p.payment_method,
       p.amount_cents, p.currency, p.status, p.source, p.occurred_at
FROM transactions p
WHERE p.source = 'processor'
  AND p.status = 'approved'
  AND p.occurred_at BETWEEN :from AND :to
  AND NOT EXISTS (
    SELECT 1 FROM transactions m
    WHERE m.source = 'merchant_order_system'
      AND m.transaction_id = p.transaction_id
  )
GROUP BY p.transaction_id;
```

### Ghost

`merchant.status='approved'` in window AND (no processor row OR latest processor status by `MAX(occurred_at)` ∈ {`declined`,`failed`}).

### Duplicates

```sql
SELECT transaction_id, source, COUNT(*) AS rows
FROM transactions
GROUP BY transaction_id, source
HAVING rows > 1
   AND EXISTS (
     SELECT 1 FROM transactions x
     WHERE x.transaction_id = transactions.transaction_id
       AND x.source = transactions.source
       AND x.occurred_at BETWEEN :from AND :to
   );
```

The detail response includes every row in the group.

### Pending limbo

```sql
SELECT ...
FROM transactions
WHERE source = 'processor'
  AND status = 'pending'
  AND payment_method IN ('pix','boleto')
  AND occurred_at BETWEEN :from AND :to
  AND ((payment_method='pix'    AND :now - occurred_at > :pix_threshold)
    OR (payment_method='boleto' AND :now - occurred_at > :boleto_threshold));
```

`credit_card pending` never counts. `merchant_order_system` pending never counts.

---

## 9. Test data and ground-truth oracle

### `cmd/seed`

Deterministic generator with flags `--seed`, `--total>=500`, `--out`, `--ingest-url`. With `--seed=42`:

- 500+ transactions across a 6-hour window.
- Mix: 60% credit_card, 30% pix, 10% boleto. 4 processors (ProcessorA..D).
- Status distribution: processor ~85/12/3, merchant ~87/10/3.
- Injected anomalies (exact counts, deterministic IDs):
  - 20 orphaned
  - 15 ghost
  - 8 duplicate **groups** (2–3 rows each)
  - 10 pending limbo (mix PIX > 24h and Boleto > 72h)

### `testdata/expected_counts.json` (oracle)

Format includes counts **and IDs** so tests assert set equality, not just numbers:

```json
{
  "counts": {
    "orphaned": 20,
    "ghost": 15,
    "duplicate_groups": 8,
    "duplicate_extra_records": 12,
    "pending_limbo": 10,
    "total_distinct_transaction_ids": 488
  },
  "ids": {
    "orphaned":      ["tx_orphan_001", "..."],
    "ghost":         ["tx_ghost_001",  "..."],
    "duplicate":     ["tx_dup_001",    "..."],
    "pending_limbo": ["tx_limbo_pix_001", "tx_limbo_boleto_001", "..."]
  },
  "expected_health_score": 0.9098
}
```

`cmd/seed` writes this file alongside `transactions.json`. Both files are committed.

---

## 10. Testing strategy (pyramid)

Coverage targets: `internal/service` ≥ 90%, `internal/repository/sqlite` ≥ 80%, `internal/httpapi` ≥ 75%, global ≥ 80%. **Do not chase 100%.**

### Unit (no I/O)
- `domain`: validation matrix, valid/invalid enums, transaction_id, occurred_at zero, amount ≤ 0, currency ≠ BRL.
- `clock`: `fakeClock` advances deterministically.
- `config`: defaults + env overrides + duration parsing.
- `service`: with **fake repo (maps)** covering every edge case in §11.

### Integration (SQLite `:memory:`)
- Load `testdata/transactions.json` via `InsertBatch`, then run real queries.
- `TestOracle_FullDataset_MatchesCountsAndIDs`: counts + **ID set equality** (`assert.ElementsMatch`) against `expected_counts.json`. **This single test guards the 55 functional+accuracy points.**
- `TestWindowBoundaries_*`: inclusive `from`/`to`, 1ms outside excluded, processor-23:59 vs merchant-00:01 (counterpart-outside-window must NOT cause orphaned).
- `TestDuplicateSemantics_*`: `groups` vs `extra_records`, key is `(transaction_id, source)` not `transaction_id` alone, same `tx_id` once in processor + once in merchant is **not** a duplicate.
- `TestPendingLimbo_Boundaries` with `fakeClock`: PIX exactly at 24h does not count (`>` strict), 24h+1ns counts; same for Boleto at 72h. `credit_card pending` and `merchant_order_system pending` never count.

### HTTP (`net/http/httptest`)
- Real service + `:memory:` repo cabled for each handler.
- Codes: 200, 400 (bad input), 415 (wrong content-type), 422 (`from > to`), 500.
- Assertions on `application/problem+json` content-type and required fields.
- **Golden response tests** for `GET /v1/health` (no filter), and `GET /v1/anomalies?type=...` for each type. Snapshots in `testdata/golden/`. Arrays sorted by `transaction_id` for normalization. `ingested_at` redacted/zeroed. `-update` flag regenerates them.

### E2E (`//go:build e2e`)
- `httptest.Server` wired exactly like `cmd/server/main.go`.
- POST the full `transactions.json` → query each anomaly type → assert **counts + IDs + health_score** against the oracle, plus body shape against goldens.
- This test proves the milestone end-to-end, no manual steps.

### Static quality
```
make test         # go test ./... -race -count=1
make test-cover   # coverprofile + cover -func
make test-e2e     # go test ./e2e -tags=e2e -race
make vet          # go vet ./...
make fmt-check    # test -z "$(gofmt -l .)"
make qa           # fmt-check + vet + test + test-e2e
make qa-docker    # build Dockerfile.test which runs the test suite inside Docker
make acceptance   # docs/examples/verify.sh against the running service
```

No GitHub Actions. The reviewer runs `make qa` locally or through Docker.

---

## 11. Edge cases that must be covered with explicit tests

- Same `transaction_id` with different amounts across sources.
- Same `transaction_id` with different `payment_method` across sources.
- Processor `approved` then `refunded`: still orphaned if no merchant record (documented).
- Processor `declined` then `approved`: ghost detection uses **most recent**.
- A transaction appears in multiple anomaly categories simultaneously (allowed).
- `amount_cents <= 0`, `currency != BRL`, invalid enum values → `400`.
- Batch with one invalid row → atomic rejection, `errors[]` includes offending index.
- `from > to` → `422`. Negative `limit`/`offset` → `400`.
- Empty dataset → `200`, `score = 1.0`, all counts `0`.
- Pending limbo decisions use the injected `Clock`, never `time.Now()`.

---

## 12. Dockerization (single-command demo)

### Goals
- `docker compose up` starts API and auto-seeds it; reviewer hits `localhost:8080` with curls.
- Final image is distroless static — no shell, non-root.
- WAL-mode SQLite persisted in a named volume.

### Key decisions
- Multi-stage `Dockerfile` with `ARG TARGET` so the same Dockerfile builds `cmd/server`, `cmd/seed`, and `cmd/healthcheck`.
- Healthcheck uses a tiny `cmd/healthcheck` Go binary (distroless lacks `wget`/`curl`).
- `seeder` service waits for `api` healthy, runs once, exits 0.
- `.dockerignore` excludes `.git`, `.cursor`, `*.db`, `coverage.out`, `docs/`, `README.md`, `**/*_test.go`. **`testdata/` is kept** so the seeder image bundles it. `go build` ignores `_test.go` automatically.
- Run-level idempotence: seeder checks `/data/.seeded` sentinel before running; creates it on completion. Reset = `make docker-reset` (= `docker compose down -v`). The seed itself is **not** idempotent at the row level — duplicate rows are intentional anomalies.
- Optional `Dockerfile.test` exposes `make qa-docker` for reviewers without Go installed.

### Compose services (summary)

```
services:
  api:     distroless static, port 8080, volume /data, healthcheck via /healthcheck binary
  seeder:  same image w/ TARGET=seed, depends_on api healthy, runs cmd/seed --ingest-url, restart: "no"
volumes:
  yuno-data
```

---

## 13. Configuration (env vars + defaults)

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | HTTP listen port. |
| `SQLITE_DSN` | `file:yuno.db?_pragma=journal_mode(WAL)` | SQLite DSN. In Docker: `file:/data/yuno.db?...`. |
| `PIX_PENDING_THRESHOLD` | `24h` | Pending limbo threshold for PIX. |
| `BOLETO_PENDING_THRESHOLD` | `72h` | Pending limbo threshold for Boleto. |
| `ALERT_ORPHANED_THRESHOLD` | `50` | Stretch alerting. |
| `ALERT_GHOST_THRESHOLD` | `100` | Stretch alerting. |
| `ALERT_HEALTH_MIN` | `0.95` | Stretch alerting. |
| `LOG_LEVEL` | `info` | `slog` level. |

Durations parse via `time.ParseDuration`.

---

## 14. Coding conventions

- **Formatting**: `gofmt`. Imports grouped stdlib / third-party / internal.
- **Errors**: typed sentinels in `domain/errors.go`; wrap with `fmt.Errorf("context: %w", err)`. Never compare error strings.
- **Context**: every repository and service method takes `ctx context.Context` first.
- **Money**: `int64` cents only. Never `float64` for monetary amounts.
- **Time**: domain stores `time.Time` UTC; HTTP layer parses/formats RFC3339; pending-limbo math uses `Clock`.
- **Logging**: `slog` JSON; no `fmt.Println`. Include `request_id` via chi middleware.
- **HTTP errors**: produce `application/problem+json`; never leak raw error strings — use `detail` fields with sanitized messages.
- **Comments**: explain *why*, not *what*. Avoid narrating obvious code.
- **No global state**: dependencies are injected through constructors (`New...`).
- **Tests**: table-driven where it helps. Use `t.Run` for subtests. Race detector always on.

---

## 15. Things that are intentionally out of scope

- Authentication / authorization.
- UI / frontend.
- Cloud deployment, Kubernetes, CI pipelines.
- Postgres or other RDBMS (the repository interface allows swapping later).
- Streaming ingestion, message queues.
- A migration tool — `Migrate(ctx)` with embedded `schema.sql` is enough for the MVP.
- Postman collection — `docs/examples/curl.sh` is the canonical demo.
- 100% test coverage — targets are pragmatic (see §10).

---

## 16. Acceptance checklist (mapped to the rubric)

- **Functional completeness (30)** + **Anomaly accuracy (25)** → integration test asserts counts **and IDs** against the oracle; e2e + golden responses corroborate.
- **API design (15)** → httptest covers 2xx/4xx/5xx, problem+json, golden body snapshots.
- **Code quality (10)** → `go vet`, `gofmt`, `go test -race`, layered design with interfaces.
- **Test data realism (8)** → 500+ rows, all required mixes and injected anomalies, deterministic with `--seed=42`, oracle committed.
- **Documentation (10)** → README "Quickstart" with `docker compose up`, `ARCHITECTURE.md` covers window semantics, anomaly queries, score formula, tradeoffs.
- **Stretch (2)** → breakdowns by processor / payment method on `/v1/health` and `/v1/anomalies`, `/v1/alerts` endpoint with threshold rules.

---

## 17. Definition of "milestone functional"

A reviewer running the following sequence sees every count match the oracle:

```bash
docker compose up --build         # API + auto-seed
bash docs/examples/curl.sh        # exercises every Core Requirement
bash docs/examples/verify.sh      # diffs responses against expected_counts.json
make qa-docker                    # runs the full test suite inside Docker
```

Anything beyond this point (breakdowns, alerts, extra polish) is incremental value and may be skipped under time pressure without compromising the Core Requirements.
