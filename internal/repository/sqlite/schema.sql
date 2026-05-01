CREATE TABLE IF NOT EXISTS transactions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    transaction_id  TEXT NOT NULL,
    occurred_at     TEXT NOT NULL,
    ingested_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    amount_cents    INTEGER NOT NULL CHECK (amount_cents > 0),
    currency        TEXT NOT NULL,
    payment_method  TEXT NOT NULL CHECK (payment_method IN ('credit_card','pix','boleto')),
    processor       TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('approved','declined','pending','failed','refunded','chargedback')),
    source          TEXT NOT NULL CHECK (source IN ('processor','merchant_order_system'))
);

CREATE INDEX IF NOT EXISTS idx_transactions_tx_id            ON transactions(transaction_id);
CREATE INDEX IF NOT EXISTS idx_transactions_tx_id_source     ON transactions(transaction_id, source);
CREATE INDEX IF NOT EXISTS idx_transactions_occurred_at      ON transactions(occurred_at);
CREATE INDEX IF NOT EXISTS idx_transactions_source_status    ON transactions(source, status);
CREATE INDEX IF NOT EXISTS idx_transactions_processor        ON transactions(processor);
CREATE INDEX IF NOT EXISTS idx_transactions_payment_method   ON transactions(payment_method);
