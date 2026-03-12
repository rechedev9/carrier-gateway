-- Migration 001: initial quotes table
-- Stores carrier quote results keyed by (request_id, carrier_id).
-- expires_at is indexed for efficient purge of stale rows.

CREATE TABLE IF NOT EXISTS quotes (
    request_id    TEXT    NOT NULL,
    carrier_id    TEXT    NOT NULL,
    premium_cents BIGINT  NOT NULL,
    currency      TEXT    NOT NULL DEFAULT 'USD',
    expires_at    TIMESTAMPTZ NOT NULL,
    is_hedged     BOOLEAN NOT NULL DEFAULT FALSE,
    latency_ms    BIGINT  NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (request_id, carrier_id)
);

CREATE INDEX IF NOT EXISTS quotes_expires_at_idx ON quotes (expires_at);
