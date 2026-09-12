-- +goose Up
-- Durable channel ingress: every normalized inbound event lands here before
-- any agent work starts. The dedup key is a historical receive coordinate —
-- channel_id and source_account_key deliberately do NOT reference `channel`
-- or channel_identity, because a deleted/renamed config must not release the
-- dedup record and let the platform's redelivery re-execute the event.
CREATE TABLE channel_inbox (
    id                 UUID PRIMARY KEY DEFAULT uuidv7(),
    -- Configured channel instance id at receive time.
    channel_id         TEXT NOT NULL,
    -- Bot account identity within the channel instance (see plan D1.2):
    -- platform account id where the platform exposes one, else the stable
    -- configured account label. Part of the dedup key so two bot accounts on
    -- one instance cannot collide.
    source_account_key TEXT NOT NULL,
    -- Stable platform event id (message id, callback id, update id...). The
    -- adapter must refuse events it cannot key rather than storing ''.
    event_key          TEXT NOT NULL,
    -- message / message_edit / callback / command / ... Valid values in Go.
    event_kind         TEXT NOT NULL,
    -- Version of the normalized payload shape stored in `payload`.
    payload_version    INTEGER NOT NULL,
    -- Normalized IncomingMessage snapshot (attachments as platform
    -- references + locally staged media paths, not inline bytes).
    payload            JSONB NOT NULL DEFAULT '{}',
    -- Receive order within this channel, assigned under the channel row lock
    -- so concurrent adapters cannot interleave. Monotonic, no gaps required.
    ingress_seq        BIGINT NOT NULL,
    -- Physical chat the event belongs to ('' for channel-level events). The
    -- router processes a channel's pending events in (chat_key, ingress_seq)
    -- order so per-chat ordering survives replica handoff.
    chat_key           TEXT NOT NULL DEFAULT '',
    -- received / ready / routed / rejected / failed. In Go.
    state              TEXT NOT NULL DEFAULT 'received',
    -- Stable machine-readable reason when state is rejected/failed.
    error_code         TEXT,
    -- Deferred routing: router skips rows whose next_attempt_at is in the
    -- future (e.g. waiting on dependency or backoff).
    next_attempt_at    TIMESTAMPTZ,
    routed_at          TIMESTAMPTZ,
    received_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (channel_id, source_account_key, event_key),
    UNIQUE (channel_id, ingress_seq)
);

-- Router scan: pending events of one channel, per chat, in receive order.
CREATE INDEX idx_channel_inbox_pending ON channel_inbox (channel_id, chat_key, ingress_seq)
    WHERE state IN ('received', 'ready');

-- +goose Down
DROP TABLE channel_inbox;
