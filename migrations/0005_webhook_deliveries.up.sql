-- Phase 1 / Lane 5 (WIREUP): persistent dedupe + audit trail for inbound
-- webhook deliveries. Replaces the in-memory MemoryDedupeStore Lane 4
-- ships in `internal/webhook/dedupe.go`. Schema is panel-aligned with
-- WEBHOOK_AND_EVENT_CONTRACT.md §2.3 (verification flow / dedupe by
-- (provider, delivery_id, event_hash) UNIQUE).

CREATE TABLE webhook_deliveries (
    id            TEXT PRIMARY KEY,        -- ULID, prefix wbd_
    source        TEXT NOT NULL,           -- github | gitlab | bitbucket | midnight
    delivery_id   TEXT NOT NULL,           -- provider-supplied delivery id
    event_type    TEXT NOT NULL,
    event_hash    TEXT NOT NULL,           -- sha256 of canonical payload
    hmac_verified BOOLEAN NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at  TIMESTAMPTZ,
    outcome       TEXT NOT NULL,           -- accepted | rejected_replay | rejected_signature | rejected_other
    reason_code   TEXT,                    -- typed failure reason when outcome != accepted
    payload_hash  TEXT NOT NULL,
    UNIQUE (source, delivery_id),
    UNIQUE (source, event_hash)
);

CREATE INDEX webhook_deliveries_received_at_idx
    ON webhook_deliveries (received_at DESC);
CREATE INDEX webhook_deliveries_outcome_idx
    ON webhook_deliveries (outcome);
