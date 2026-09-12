-- +goose Up
-- Durable outbound record: one row per platform operation (send / edit /
-- delete / reaction / attachment upload). The payload is frozen before the
-- first send attempt so every retry and every owner performs byte-identical
-- work. channel_id/source_account_key are historical coordinates like
-- channel_inbox — no FK, deleting a channel config must not erase the send
-- ledger.
CREATE TABLE channel_outbox (
    id                 UUID PRIMARY KEY DEFAULT uuidv7(),
    -- Producing run; NULL for operations minted outside a run (group publish,
    -- Notify). CASCADE so session deletion drops its pending ledger.
    run_id             UUID REFERENCES agent_run(id) ON DELETE CASCADE,
    -- Groups the operations of one logical delivery (one reply, one
    -- notification). Callers keep it stable across retries.
    delivery_key       TEXT NOT NULL,
    -- Position inside the delivery: the unit of platform idempotency.
    operation_index    INTEGER NOT NULL CHECK (operation_index >= 0),
    -- send_text / edit_text / send_attachment / delete / reaction / typing.
    -- Valid values in Go.
    operation_kind     TEXT NOT NULL,
    channel_id         TEXT NOT NULL,
    source_account_key TEXT NOT NULL,
    -- Versioned target coordinates: chat, thread, reply anchor, card/inline
    -- keyboard refs needed to reconstruct the send on another replica.
    address            JSONB NOT NULL DEFAULT '{}',
    -- Frozen send body: normalized text or staged local media reference the
    -- sender re-reads at send time.
    payload            JSONB NOT NULL DEFAULT '{}',
    -- operation_index list within the same delivery_key that must reach a
    -- terminal state first (e.g. edit after its send). [] = no dependency.
    depends_on         JSONB NOT NULL DEFAULT '[]',
    -- pending / sending / sent / failed / unknown / canceled. In Go.
    state              TEXT NOT NULL DEFAULT 'pending',
    -- Single attempt fence: claim sets it, completion updates compare it.
    attempt_token      UUID,
    -- runtime_token of the channel owner allowed to send. Stale-owner sends
    -- are refused, so a fenced-out replica cannot emit a duplicate after
    -- losing its lease.
    owner_token        UUID,
    attempt_started_at TIMESTAMPTZ,
    next_attempt_at    TIMESTAMPTZ,
    -- Platform's id for the produced message, once known; enables edit/delete
    -- follow-ups and unknown-result probes.
    platform_message_id TEXT,
    error_code         TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (delivery_key, operation_index)
);

-- Owner's send loop: due pending ops for channels it owns.
CREATE INDEX idx_channel_outbox_pending ON channel_outbox (channel_id, next_attempt_at)
    WHERE state = 'pending';
-- Recovery scans: sending rows whose attempt lease died, unknown awaiting probe.
CREATE INDEX idx_channel_outbox_recovery ON channel_outbox (attempt_started_at)
    WHERE state IN ('sending', 'unknown');
CREATE INDEX idx_channel_outbox_run ON channel_outbox (run_id) WHERE run_id IS NOT NULL;

-- +goose Down
DROP TABLE channel_outbox;
