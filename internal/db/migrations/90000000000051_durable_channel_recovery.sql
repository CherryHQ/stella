-- +goose Up
SET LOCAL lock_timeout = '10s';

-- GroupRoute is a classification lease over the canonical group sequence.  It
-- is intentionally separate from AgentRun and from ctx_group_dispatch: the
-- row proves which claimant won classification, while responder execution
-- remains owned by the existing dispatch rows.
CREATE TABLE channel_group_route (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    group_message_id UUID NOT NULL REFERENCES ctx_group_message(id) ON DELETE RESTRICT,
    group_id UUID NOT NULL REFERENCES ctx_group_state(id) ON DELETE RESTRICT,
    group_seq BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    claim_token UUID,
    claim_expires_at TIMESTAMPTZ,
    decisions JSONB NOT NULL DEFAULT '[]'::jsonb,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT channel_group_route_seq_positive CHECK (group_seq > 0),
    CONSTRAINT channel_group_route_status_valid CHECK (status IN ('pending', 'claimed', 'completed')),
    CONSTRAINT channel_group_route_claim_shape CHECK (
        (status = 'pending' AND claim_token IS NULL AND claim_expires_at IS NULL AND completed_at IS NULL)
        OR (status = 'claimed' AND claim_token IS NOT NULL AND claim_expires_at IS NOT NULL AND completed_at IS NULL)
        OR (status = 'completed' AND claim_token IS NULL AND claim_expires_at IS NULL AND completed_at IS NOT NULL)
    ),
    UNIQUE (group_message_id),
    UNIQUE (group_id, group_seq)
);

CREATE INDEX idx_channel_group_route_claim
    ON channel_group_route(group_id, group_seq, status, claim_expires_at);

-- A channel binding is the durable lane for one physical chat.  The process
-- local session queue remains a fairness optimisation; this row and its
-- monotonic revision are the ordering authority after a restart.
CREATE TABLE channel_binding (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    channel_id TEXT NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    platform TEXT NOT NULL,
    chat_key TEXT NOT NULL,
    thread_key TEXT NOT NULL DEFAULT '',
    principal_kind TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    agent_id TEXT NOT NULL DEFAULT '',
    session_id TEXT REFERENCES ctx_conversation(session_id) ON DELETE RESTRICT,
    revision BIGINT NOT NULL DEFAULT 1,
    next_seq BIGINT NOT NULL DEFAULT 1,
    state TEXT NOT NULL DEFAULT 'active',
    max_rows BIGINT NOT NULL DEFAULT 1000,
    max_bytes BIGINT NOT NULL DEFAULT 67108864,
    accepted_rows BIGINT NOT NULL DEFAULT 0,
    accepted_bytes BIGINT NOT NULL DEFAULT 0,
    released_rows BIGINT NOT NULL DEFAULT 0,
    released_bytes BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT channel_binding_revision_positive CHECK (revision > 0),
    CONSTRAINT channel_binding_next_seq_positive CHECK (next_seq > 0),
    CONSTRAINT channel_binding_quota_nonnegative CHECK (
        max_rows > 0 AND max_bytes > 0 AND accepted_rows >= released_rows
        AND accepted_bytes >= released_bytes
    ),
    CONSTRAINT channel_binding_identity_present CHECK (
        platform <> '' AND chat_key <> '' AND principal_kind <> '' AND principal_id <> ''
    ),
    CONSTRAINT channel_binding_state_valid CHECK (state IN ('active', 'blocked', 'retired')),
    UNIQUE (channel_id, platform, chat_key, thread_key)
);

CREATE INDEX idx_channel_binding_active
    ON channel_binding(channel_id, state)
    WHERE state = 'active';

-- Expiring platform reply routes are encrypted at ingress and resolved only
-- by a durable publisher.  The opaque id is carried in the group envelope;
-- plaintext webhook/session secrets never enter event-log or FIFO payloads.
CREATE TABLE channel_reply_capability (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    channel_id TEXT NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    ciphertext TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT channel_reply_capability_present CHECK (
        kind <> '' AND octet_length(kind) <= 64
        AND ciphertext <> '' AND octet_length(ciphertext) <= 65536
    ),
    CONSTRAINT channel_reply_capability_expiry CHECK (expires_at > created_at)
);

CREATE INDEX idx_channel_reply_capability_live
    ON channel_reply_capability(channel_id, kind, expires_at);

-- Principal and deployment quotas are separate durable rows because several
-- physical chats can share one linked user/group, and several bindings can
-- share one configured channel instance.  released_* is deliberately kept as
-- an audit counter: live usage is accepted_* - released_* and terminalization
-- can be proven idempotent from the item row's released_at.
CREATE TABLE channel_principal_quota (
    principal_key TEXT PRIMARY KEY,
    principal_kind TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    max_rows BIGINT NOT NULL DEFAULT 10000,
    max_bytes BIGINT NOT NULL DEFAULT 536870912,
    accepted_rows BIGINT NOT NULL DEFAULT 0,
    accepted_bytes BIGINT NOT NULL DEFAULT 0,
    released_rows BIGINT NOT NULL DEFAULT 0,
    released_bytes BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT channel_principal_quota_key_present CHECK (
        principal_key <> '' AND principal_kind <> '' AND principal_id <> ''
    ),
    CONSTRAINT channel_principal_quota_nonnegative CHECK (
        max_rows > 0 AND max_bytes > 0 AND accepted_rows >= released_rows
        AND accepted_bytes >= released_bytes
    ),
    UNIQUE (principal_kind, principal_id)
);

CREATE TABLE channel_deployment_quota (
    -- One deployment-wide row.  Keep the stable key explicit so a new
    -- channel instance cannot mint another copy of the deployment budget.
    channel_id TEXT PRIMARY KEY DEFAULT 'deployment',
    max_rows BIGINT NOT NULL DEFAULT 100000,
    max_bytes BIGINT NOT NULL DEFAULT 8589934592,
    accepted_rows BIGINT NOT NULL DEFAULT 0,
    accepted_bytes BIGINT NOT NULL DEFAULT 0,
    released_rows BIGINT NOT NULL DEFAULT 0,
    released_bytes BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT channel_deployment_quota_nonnegative CHECK (
        channel_id = 'deployment' AND
        max_rows > 0 AND max_bytes > 0 AND accepted_rows >= released_rows
        AND accepted_bytes >= released_bytes
    )
);

-- The payload is canonical input, never an expiring platform URL or a mutable
-- workspace path.  A source key is unique only when the platform supplied a
-- stable identity; callers without one use an opaque generated key, which
-- preserves every delivery instead of guessing deduplication from content.
CREATE TABLE channel_fifo_item (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    binding_id UUID NOT NULL REFERENCES channel_binding(id) ON DELETE CASCADE,
    -- Quota ownership is captured at admission.  A later channel rebind may
    -- advance the binding revision, but it must never release this item's
    -- bytes against the successor principal.
    principal_key TEXT NOT NULL,
    seq BIGINT NOT NULL,
    source_key TEXT NOT NULL,
    schema_version INTEGER NOT NULL DEFAULT 1,
    payload JSONB NOT NULL,
    payload_bytes BIGINT NOT NULL,
    media_bytes BIGINT NOT NULL DEFAULT 0,
    capability_bytes BIGINT NOT NULL DEFAULT 0,
    byte_cost BIGINT NOT NULL,
    command TEXT NOT NULL DEFAULT '',
    args TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'pending',
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_expires_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    run_id UUID REFERENCES agent_run(id) ON DELETE RESTRICT,
    expected_session_id TEXT,
    expected_binding_revision BIGINT,
    result_text TEXT NOT NULL DEFAULT '',
    result_handled BOOLEAN NOT NULL DEFAULT false,
    error_code TEXT NOT NULL DEFAULT '',
    error_detail TEXT NOT NULL DEFAULT '',
    rejected_by TEXT NOT NULL DEFAULT '',
    rejected_reason TEXT NOT NULL DEFAULT '',
    released_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT channel_fifo_item_seq_positive CHECK (seq > 0),
    CONSTRAINT channel_fifo_item_principal_present CHECK (principal_key <> ''),
    CONSTRAINT channel_fifo_item_schema_positive CHECK (schema_version > 0),
    CONSTRAINT channel_fifo_item_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT channel_fifo_item_sizes_valid CHECK (
        payload_bytes > 0 AND media_bytes >= 0 AND capability_bytes >= 0 AND byte_cost > 0
        AND byte_cost = payload_bytes + media_bytes + capability_bytes
    ),
    CONSTRAINT channel_fifo_item_attempt_nonnegative CHECK (attempt >= 0),
    CONSTRAINT channel_fifo_item_state_valid CHECK (
        state IN ('pending', 'running', 'blocked', 'completed', 'rejected', 'discarded')
    ),
    CONSTRAINT channel_fifo_item_terminal_shape CHECK (
        (state IN ('completed', 'rejected', 'discarded') AND released_at IS NOT NULL)
        OR (state NOT IN ('completed', 'rejected', 'discarded') AND released_at IS NULL)
    ),
    UNIQUE (binding_id, seq)
);

CREATE UNIQUE INDEX idx_channel_fifo_item_stable_source
    ON channel_fifo_item(binding_id, source_key)
    WHERE source_key <> '';
CREATE INDEX idx_channel_fifo_item_claim
    ON channel_fifo_item(binding_id, seq, next_attempt_at)
    WHERE state IN ('pending', 'running');
CREATE INDEX idx_channel_fifo_item_expired_lease
    ON channel_fifo_item(lease_expires_at)
    WHERE state = 'running' AND lease_expires_at IS NOT NULL;
CREATE INDEX idx_channel_fifo_item_run_id
    ON channel_fifo_item(run_id)
    WHERE run_id IS NOT NULL;

-- Direct-message reply credentials belong to the accepted FIFO item when one
-- exists.  Group ingest keeps its capability reference on the group outbox,
-- so fifo_item_id remains nullable for that source path.
ALTER TABLE channel_reply_capability
    ADD COLUMN fifo_item_id UUID REFERENCES channel_fifo_item(id) ON DELETE CASCADE;
CREATE INDEX idx_channel_reply_capability_fifo_item
    ON channel_reply_capability(fifo_item_id)
    WHERE fifo_item_id IS NOT NULL;

-- Media references are deliberately separate from the payload.  ctx_media is
-- the owner-scoped immutable content-addressed object; this table preserves
-- filename/mime/size presentation facts for the channel envelope.
CREATE TABLE channel_fifo_media (
    item_id UUID NOT NULL REFERENCES channel_fifo_item(id) ON DELETE CASCADE,
    media_id UUID NOT NULL REFERENCES ctx_media(id) ON DELETE RESTRICT,
    file_name TEXT NOT NULL DEFAULT '',
    mime_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (item_id, media_id),
    CONSTRAINT channel_fifo_media_size_positive CHECK (size_bytes > 0),
    CONSTRAINT channel_fifo_media_mime_present CHECK (mime_type <> '')
);

-- Explicit poison-head rejection is an operator action, not an automatic
-- dead-letter transition.  Keeping an append-only audit row makes the release
-- of quota and the identity/reason visible after the item becomes terminal.
CREATE TABLE channel_fifo_rejection (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    item_id UUID NOT NULL REFERENCES channel_fifo_item(id) ON DELETE CASCADE,
    operator_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT channel_fifo_rejection_present CHECK (operator_id <> '' AND reason <> '')
);

CREATE INDEX idx_channel_fifo_rejection_item ON channel_fifo_rejection(item_id, created_at);

-- The old receipt remains the public idempotency record for /new.  Linking it
-- to the durable item lets historical receipts and new FIFO barriers share one
-- authority without changing the physical source key.
ALTER TABLE channel_chat_command_receipt
    ADD COLUMN fifo_item_id UUID REFERENCES channel_fifo_item(id) ON DELETE SET NULL,
    ADD COLUMN expected_session_id TEXT,
    ADD COLUMN binding_revision BIGINT;
CREATE UNIQUE INDEX idx_channel_chat_command_receipt_fifo_item
    ON channel_chat_command_receipt(fifo_item_id)
    WHERE fifo_item_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_channel_group_route_claim;
DROP TABLE channel_group_route;
DROP INDEX idx_channel_chat_command_receipt_fifo_item;
ALTER TABLE channel_chat_command_receipt
    DROP COLUMN binding_revision,
    DROP COLUMN expected_session_id,
    DROP COLUMN fifo_item_id;
DROP INDEX idx_channel_fifo_rejection_item;
DROP TABLE channel_fifo_rejection;
DROP TABLE channel_fifo_media;
DROP INDEX idx_channel_reply_capability_fifo_item;
ALTER TABLE channel_reply_capability DROP COLUMN fifo_item_id;
DROP INDEX idx_channel_fifo_item_run_id;
DROP INDEX idx_channel_fifo_item_expired_lease;
DROP INDEX idx_channel_fifo_item_claim;
DROP INDEX idx_channel_fifo_item_stable_source;
DROP TABLE channel_fifo_item;
DROP TABLE channel_deployment_quota;
DROP TABLE channel_principal_quota;
DROP INDEX idx_channel_binding_active;
DROP TABLE channel_binding;
DROP INDEX idx_channel_reply_capability_live;
DROP TABLE channel_reply_capability;
