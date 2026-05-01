# Architecture

## Layers

```
domain  <-  service  <-  repository (iface)  <-  httpapi
                            │
                            ├── sqlite (real impl)
                            └── fake   (in-memory impl for tests)
```

- `domain` — pure types (`Transaction`, `Anomaly*`, enums), validation,
  typed errors (`ErrInvalidInput`, `ErrNotFound`, `ErrInvalidWindow`).
  Knows nothing about HTTP or SQL.
- `service` — orchestrates validation + repository + rules (health score,
  alerts, samples). Receives `Repository` via interface and an injected
  `clock.Clock`.
- `repository` — interface + shared types (`Window`, `AnomalyFilter`,
  `DuplicateGroup`, `Counts`, `PendingLimboThresholds`). The `Fake` lives
  here so the service tests do not need SQLite.
- `repository/sqlite` — implementation backed by SQLite (`modernc.org/sqlite`,
  pure Go), schema embedded with `embed.FS`, inline SQL queries. WAL via DSN.
- `httpapi` — `chi` router, middleware (RequestID, RealIP, Recoverer,
  slog JSON Logger, 30s Timeout), handlers, DTOs, problem+json (RFC 7807).

## Data model

Table `transactions` (one event = one row):

| column           | type                | note                                                 |
| ---------------- | ------------------- | ---------------------------------------------------- |
| `id`             | `INTEGER PK AUTO`   | row PK; not the `transaction_id`                     |
| `transaction_id` | `TEXT NOT NULL`     | business ID; can repeat (duplicate detection)        |
| `occurred_at`    | `TEXT NOT NULL`     | RFC3339 with nanos, always UTC                       |
| `ingested_at`    | `TEXT NOT NULL`     | default `strftime('%Y-%m-%dT%H:%M:%fZ','now')`       |
| `amount_cents`   | `INTEGER`           | `> 0` (CHECK). Money as `int64`, **never** float     |
| `currency`       | `TEXT`              | validated in domain to `BRL`                         |
| `payment_method` | `TEXT`              | `credit_card | pix | boleto` (CHECK + domain enum)   |
| `processor`      | `TEXT`              | free-form, indexed                                   |
| `status`         | `TEXT`              | `approved|declined|pending|failed|refunded|chargedback` |
| `source`         | `TEXT`              | `processor | merchant_order_system`                  |

Indexes: `(transaction_id)`, `(transaction_id, source)`, `(occurred_at)`,
`(source, status)`, `(processor)`, `(payment_method)`.

### Why the PK is `id` and not `transaction_id`

The four anomalies require seeing multiple rows with the same
`transaction_id` (duplicates within the same source, ghost crossing
sources, ghost looking at the latest processor status, etc.). A PK on
`transaction_id` would force an upsert that loses historical information.

### Why `chargedback` is accepted but does not impact the MVP

The brief mentions it as a business status. We accept it so ingest does
not reject legitimate events, but none of the four MVP anomalies use
`chargedback`. If in the future we want "approved followed by chargedback
with financial impact > X" we add it as a new anomaly without breaking
the schema.

### Why `int64` cents and not `decimal`

Comparison, sum, and grouping operations in SQLite are exact with
`INTEGER`. The JSON parser never converts to float when the field is
`int64`. Nothing in the domain multiplies/divides amounts.

## Time-window semantics (important)

`from` and `to` in RFC3339, both optional. If both are present and
`from > to` -> `422 Unprocessable Entity`. If absent the whole dataset is
analyzed.

To avoid false positives at boundaries, **the window is applied only to
the primary side of each anomaly and the counterpart lookup spans the
ENTIRE dataset**:

| Anomaly        | Primary side filtered by window                | Counterpart side (no filter)             |
| -------------- | ---------------------------------------------- | ---------------------------------------- |
| Orphaned       | processor approved                             | merchant (any timestamp)                 |
| Ghost          | merchant approved                              | processor (any timestamp + latest status)|
| Duplicate      | `(tx_id, source)` groups with >=1 row in window | rest of the group (any timestamp)        |
| Pending Limbo  | processor pending pix/boleto                    | injected clock (`now - occurred_at > threshold`) |

Without this, a transaction with processor at 23:59 and merchant at 00:01
the next day would surface as orphaned when filtering by the daily
window, which is a false positive.

## Anomaly queries (SQLite)

### Orphaned Processor Approvals

```sql
SELECT p.transaction_id, p.processor, p.payment_method, p.amount_cents,
       p.currency, p.status, p.source, MIN(p.occurred_at) AS occurred_at
FROM transactions p
WHERE p.source = 'processor' AND p.status = 'approved'
  AND p.occurred_at >= ? AND p.occurred_at <= ?
  AND NOT EXISTS (
    SELECT 1 FROM transactions m
    WHERE m.source = 'merchant_order_system'
      AND m.transaction_id = p.transaction_id
  )
GROUP BY p.transaction_id;
```

A single record per `transaction_id` even if there are multiple processor
events. `MIN(occurred_at)` gives a stable representation.

### Ghost Orders

```sql
WITH proc_latest AS (
  SELECT t.transaction_id, t.status
  FROM transactions t
  JOIN (
    SELECT transaction_id, MAX(occurred_at) AS max_at
    FROM transactions
    WHERE source = 'processor'
    GROUP BY transaction_id
  ) lx ON lx.transaction_id = t.transaction_id AND lx.max_at = t.occurred_at
  WHERE t.source = 'processor'
)
SELECT m.transaction_id, ..., COALESCE(p.status, '') AS proc_status
FROM transactions m
LEFT JOIN proc_latest p ON p.transaction_id = m.transaction_id
WHERE m.source = 'merchant_order_system' AND m.status = 'approved'
  AND m.occurred_at >= ? AND m.occurred_at <= ?
  AND (p.status IS NULL OR p.status IN ('declined','failed'))
GROUP BY m.transaction_id;
```

`proc_latest` is a materialized subquery with the latest processor status
per `transaction_id`. This covers the case "processor declined ->
approved" (latest status = approved -> not ghost) and "approved ->
declined" (latest status = declined -> IS ghost).

### Duplicate Submissions

```sql
SELECT transaction_id, source, COUNT(*) AS cnt
FROM transactions
GROUP BY transaction_id, source
HAVING COUNT(*) > 1
   AND EXISTS (
     SELECT 1 FROM transactions inner_t
     WHERE inner_t.transaction_id = transactions.transaction_id
       AND inner_t.source = transactions.source
       AND inner_t.occurred_at >= ? AND inner_t.occurred_at <= ?
   );
```

For each group's detail, we run a second query by
`(transaction_id, source)` to fetch every row (including those outside
the window). This gives the client context — the full group, not just
the row that falls in the window.

### Pending Limbo

```sql
SELECT ... FROM transactions t
WHERE t.source = 'processor' AND t.status = 'pending'
  AND (
    (t.payment_method = 'pix'    AND t.occurred_at < ?) OR  -- now - PIX_THRESHOLD
    (t.payment_method = 'boleto' AND t.occurred_at < ?)     -- now - BOLETO_THRESHOLD
  )
  AND t.occurred_at >= ? AND t.occurred_at <= ?;
```

`now` comes from the injected `clock.Clock`. Thresholds come from config
(default 24h pix, 72h boleto). Tests use `clock.Fake` to avoid depending
on `time.Now()`.

#### Boundary semantics

The cutoff comparison is **strict `<`** (i.e. `now - occurred_at > threshold`):

- A PIX with `now - occurred_at == 24h` (to the nanosecond) does **not**
  count. Tested by `TestRepository_PendingLimbo_PixExactly24hDoesNotCount`.
- The same row at `24h + 1ns` does count
  (`TestRepository_PendingLimbo_PixOneNanoPast24hCounts`). Boleto
  follows the same rule at 72h.
- `payment_method='credit_card'` with `pending` status **never** counts,
  regardless of age (credit-card timeouts are not a Yuno reconciliation
  signal). Tested by `TestRepository_PendingLimbo_CreditCardNeverCounts`.
- `source='merchant_order_system'` with `pending` **never** counts —
  pending limbo is only meaningful on the processor side. Tested by
  `TestRepository_PendingLimbo_MerchantPendingNeverCounts`.

The strict `<` choice keeps the contract crisp: "the row has been pending
for *strictly more than* the threshold". Switching to `<=` would shift
detection one nanosecond earlier, which the spec does not require and
which would surprise reviewers reading the threshold config.

#### Timestamp serialization

SQLite stores timestamps as TEXT in the canonical fixed-width format
`2006-01-02T15:04:05.000000000Z` (RFC 3339 with **nine fixed nanosecond
digits**, always UTC). The fixed width matters: lexicographic comparisons
(`<`, `<=`, `>=`) are then equivalent to chronological ones across the
full nanosecond range, including boundary cases like
`23:59:59.999999999Z` vs `00:00:00.000000000Z` of the next day.

A trimmed-zero format like `.999999999` (Go's default) would break this:
`Z` (0x5A) sorts after `.` (0x2E), so a row stored as `"…:00Z"` would
compare *greater* than `"…:00.000000001Z"`, silently flipping window
boundary tests. We learned this the hard way; see
`TestRepository_WindowBoundaries_OneNanoOutsideExcluded`.

## Health score

```
duplicates_extra = total_duplicate_rows - duplicate_groups
anomalies_total  = orphaned + ghost + duplicates_extra + pending_limbo
denominator      = COUNT(DISTINCT transaction_id)  -- in the window
score            = clamp(1 - anomalies_total/denominator, 0, 1)
```

- `denominator == 0` -> `score = 1.0`. An empty dataset is considered
  healthy; documented to avoid surprises.
- `duplicate_groups` is what is shown in `anomaly_counts.duplicate`
  (a human-friendly count: "8 repeated groups"). `duplicates_extra` is
  what weighs on the score (it reflects the actual number of redundant
  rows).
- Anomalies are **not mutually exclusive**. A transaction can be both
  duplicate AND orphaned. Documented and covered by tests.

## Atomic ingest

The batch endpoint validates **all** transactions before starting to
write. If any fails validation, it returns `400` with `errors[]` (index
+ reason) and persists nothing. This is more valuable than partial
ingestion for retrying pipelines: the client knows exactly what to fix.

The insertion uses a single `BEGIN/COMMIT`, so if it fails halfway (DB
down, CHECK violated, etc.) the rollback leaves the database consistent.

## Injected clock

`internal/clock` defines a `Clock` interface with `Now() time.Time`. The
service receives it via its constructor. The `Real` impl returns
`time.Now().UTC()`; `Fake` allows `Set` and `Advance` for deterministic
tests.

Production code **must not** call `time.Now()` directly when the value
drives logic (it can still be used for logging/metrics).

## Errors -> HTTP

| Domain error           | HTTP status                  | Content-Type                |
| ---------------------- | ---------------------------- | --------------------------- |
| `ErrInvalidInput`      | 400 Bad Request              | `application/problem+json`  |
| `ErrInvalidWindow`     | 422 Unprocessable Entity     | `application/problem+json`  |
| `ErrNotFound`          | 404 Not Found                | `application/problem+json`  |
| unknown                | 500 Internal Server Error    | `application/problem+json`  |

For an invalid batch, in addition to problem+json, the body includes
`errors: [{index, transaction_id, error}]` so the client can show errors
row by row.

## Tradeoffs and future work

- **Postgres / streaming.** Swapping `repository/sqlite` for
  `repository/postgres` is straightforward: queries are ANSI with the
  sole exception of the `proc_latest` CTE (still compatible). The window
  function could be replaced by
  `ROW_NUMBER() OVER (PARTITION BY transaction_id ORDER BY occurred_at DESC)`
  on Postgres to avoid the self-join. Streaming requires adding an
  `EventBus` (NATS/Kafka) and publishing anomalies as they are detected;
  the `service` does not change, only the fan-out.
- **Multi-tenant.** Add `merchant_id TEXT NOT NULL` to `transactions`,
  index `(merchant_id, transaction_id)`, and propagate it through the
  window.
- **Backfill.** Ingest is idempotent from the detector's point of view
  (anomalies are recomputed on every query) but not at the row level —
  duplicating an insertion produces artificial duplicates. For backfill,
  add an `external_id UNIQUE` column or use
  `INSERT ... ON CONFLICT DO NOTHING`.
- **New anomalies.** The layered design makes adding "approved ->
  chargedback" just a new query in `repository`, a new method on
  `AnomaliesService`, and a new `AnomalyType`. The response shape does
  not change.
- **Performance.** For large volumes (>1M rows in the window), the four
  queries are `O(n log n)` with the current indexes. If this becomes a
  bottleneck, materializing `proc_latest` as a view or a trigger-refreshed
  table is the next lever.

## Tests

- **Unit (domain, service):** in-memory fake repo. Cover the happy path
  and the edge cases listed in the plan (amount <= 0, currency, enums,
  atomic batch, invalid window, empty dataset with score=1.0,
  Clock.Fake for limbo).
- **Integration (sqlite):** open `:memory:`, migrate, load
  `testdata/transactions.json` and assert against
  `testdata/expected_counts.json`. Any future change in queries that
  alters the counts breaks these tests.
- **HTTP (httpapi):** `httptest.NewServer` with real services on top of
  the fake repo. Cover 200/201/400/422 + `Content-Type: problem+json`.
- **Seed (test/seed):** verifies that the injected anomalies are
  detectable with trivial queries in plain Go (independent of the "real"
  detection), guaranteeing the oracle does not drift from the generator.
