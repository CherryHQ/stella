-- +goose Up
SET LOCAL lock_timeout = '10s';

CREATE TABLE runtime_executor_boot (
    id UUID PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'starting',
    control_backend_pid BIGINT,
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    drained_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT runtime_executor_boot_status_valid CHECK (
        status IN ('starting', 'running', 'drained')
    ),
    CONSTRAINT runtime_executor_boot_drain_shape CHECK (
        (status = 'drained' AND drained_at IS NOT NULL)
        OR (status <> 'drained' AND drained_at IS NULL)
    )
);

CREATE TABLE agent_run (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    session_id TEXT NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE RESTRICT,
    executor_boot_id UUID NOT NULL REFERENCES runtime_executor_boot(id) ON DELETE RESTRICT,
    source TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    lease_expires_at TIMESTAMPTZ NOT NULL,
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    abort_requested_at TIMESTAMPTZ,
    abort_reason TEXT NOT NULL DEFAULT '',
    terminal_reason TEXT NOT NULL DEFAULT '',
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_run_status_valid CHECK (
        status IN ('running', 'completed', 'failed', 'canceled', 'aborted', 'interrupted')
    ),
    CONSTRAINT agent_run_source_present CHECK (source <> ''),
    CONSTRAINT agent_run_terminal_shape CHECK (
        (status = 'running' AND completed_at IS NULL)
        OR (status <> 'running' AND completed_at IS NOT NULL)
    ),
    CONSTRAINT agent_run_abort_shape CHECK (
        (abort_requested_at IS NULL AND abort_reason = '')
        OR abort_requested_at IS NOT NULL
    )
);

CREATE UNIQUE INDEX idx_agent_run_one_running_session
    ON agent_run(session_id)
    WHERE status = 'running';
CREATE INDEX idx_agent_run_running_lease
    ON agent_run(lease_expires_at)
    WHERE status = 'running';
CREATE INDEX idx_agent_run_executor
    ON agent_run(executor_boot_id, status);

CREATE TABLE agent_session_sandbox (
    session_id TEXT PRIMARY KEY REFERENCES ctx_conversation(session_id) ON DELETE CASCADE,
    generation BIGINT NOT NULL DEFAULT 0 CHECK (generation >= 0),
    state TEXT NOT NULL DEFAULT 'absent',
    executor_boot_id UUID REFERENCES runtime_executor_boot(id) ON DELETE RESTRICT,
    run_id UUID REFERENCES agent_run(id) ON DELETE RESTRICT,
    resource_backend TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    fenced_at TIMESTAMPTZ,
    destroyed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_session_sandbox_state_valid CHECK (
        state IN ('absent', 'creating', 'active', 'fenced', 'destroyed')
    ),
    CONSTRAINT agent_session_sandbox_owner_shape CHECK (
        (state = 'absent' AND executor_boot_id IS NULL AND run_id IS NULL AND resource_backend = '' AND resource_id = '')
        OR state <> 'absent'
    ),
    CONSTRAINT agent_session_sandbox_lifecycle_shape CHECK (
        (state = 'absent' AND executor_boot_id IS NULL AND run_id IS NULL AND resource_backend = '' AND resource_id = '')
        OR (state = 'creating' AND executor_boot_id IS NOT NULL AND run_id IS NOT NULL AND resource_backend <> '' AND resource_id <> '')
        OR (state = 'active' AND executor_boot_id IS NOT NULL AND run_id IS NOT NULL AND resource_backend <> '' AND resource_id <> '')
        OR (state = 'fenced' AND executor_boot_id IS NOT NULL AND run_id IS NOT NULL AND resource_backend <> '' AND resource_id <> '' AND fenced_at IS NOT NULL)
        OR (state = 'destroyed' AND executor_boot_id IS NOT NULL AND run_id IS NOT NULL AND resource_backend <> '' AND resource_id <> '' AND fenced_at IS NOT NULL AND destroyed_at IS NOT NULL)
    )
);
CREATE INDEX idx_agent_session_sandbox_executor
    ON agent_session_sandbox(executor_boot_id)
    WHERE executor_boot_id IS NOT NULL;
CREATE INDEX idx_agent_session_sandbox_run
    ON agent_session_sandbox(run_id)
    WHERE run_id IS NOT NULL;

ALTER TABLE ctx_session_inbox
    ADD COLUMN run_id UUID REFERENCES agent_run(id) ON DELETE RESTRICT;
CREATE UNIQUE INDEX idx_ctx_session_inbox_run_id
    ON ctx_session_inbox(run_id)
    WHERE run_id IS NOT NULL;

-- These immutable validators keep queue payloads self-contained even when a
-- caller bypasses the Go admission path. A deferred image reference has one
-- canonical UUID identity and every referenced byte is represented exactly
-- once in immutable_media and attachment_bytes.
-- +goose StatementBegin
CREATE FUNCTION channel_fifo_payload_is_canonical(value JSONB)
RETURNS BOOLEAN
LANGUAGE SQL
IMMUTABLE
STRICT
AS $function$
    SELECT jsonb_typeof(value) = 'array'
       AND NOT EXISTS (
           SELECT 1
           FROM jsonb_array_elements(value) AS entry(block)
           WHERE jsonb_typeof(block) <> 'object'
              OR COALESCE(block->>'kind', '') NOT IN ('text', 'image_ref', 'file_ref')
              OR CASE block->>'kind'
                   WHEN 'text' THEN
                       (block - ARRAY['kind', 'text']::TEXT[]) <> '{}'::jsonb
                       OR (block ? 'text' AND jsonb_typeof(block->'text') <> 'string')
                   WHEN 'image_ref' THEN
                       (block - ARRAY['kind', 'media_id', 'baseline']::TEXT[]) <> '{}'::jsonb
                       OR COALESCE(block->>'media_id', '') !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                       OR (block ? 'baseline' AND (
                           jsonb_typeof(block->'baseline') <> 'object'
                           OR ((block->'baseline') - ARRAY['Text']::TEXT[]) <> '{}'::jsonb
                           OR ((block->'baseline') ? 'Text' AND jsonb_typeof(block->'baseline'->'Text') <> 'string')
                       ))
                   WHEN 'file_ref' THEN
                       (block - ARRAY['kind', 'media_id', 'name', 'path']::TEXT[]) <> '{}'::jsonb
                       OR COALESCE(block->>'media_id', '') !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                       OR COALESCE(block->>'name', '') = ''
                       OR COALESCE(block->>'path', '') NOT LIKE '$STELLA_ASSETS_DIR/%'
                   ELSE TRUE
                 END
       );
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION channel_fifo_media_is_canonical(payload JSONB, metadata JSONB, total_bytes BIGINT)
RETURNS BOOLEAN
LANGUAGE SQL
IMMUTABLE
STRICT
AS $function$
    SELECT jsonb_typeof(metadata) = 'array'
       AND NOT EXISTS (
           SELECT 1
           FROM jsonb_array_elements(metadata) AS entry(item)
           WHERE jsonb_typeof(item) <> 'object'
              OR (item - ARRAY['media_id', 'size_bytes']::TEXT[]) <> '{}'::jsonb
              OR COALESCE(item->>'media_id', '') !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
              OR COALESCE(item->>'size_bytes', '') !~ '^[1-9][0-9]*$'
       )
       AND ARRAY(
           SELECT DISTINCT block->>'media_id'
           FROM jsonb_array_elements(payload) AS entry(block)
           WHERE block->>'kind' IN ('image_ref', 'file_ref')
           ORDER BY 1
       ) = ARRAY(
           SELECT item->>'media_id'
           FROM jsonb_array_elements(metadata) AS entry(item)
           ORDER BY 1
       )
       AND total_bytes = COALESCE((
           SELECT SUM(
               CASE WHEN COALESCE(item->>'size_bytes', '') ~ '^[1-9][0-9]*$'
                    THEN (item->>'size_bytes')::BIGINT
                    ELSE 0
               END
           )
           FROM jsonb_array_elements(metadata) AS entry(item)
       ), 0);
$function$;
-- +goose StatementEnd

-- Accepted asynchronous channel input is ordered by one durable ChatBinding
-- coordinate. source_key is the platform's stable delivery identity.
CREATE TABLE channel_binding_fifo (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    enqueue_seq BIGINT GENERATED ALWAYS AS IDENTITY UNIQUE,
    channel_id TEXT NOT NULL REFERENCES channel(id) ON DELETE RESTRICT,
    binding_key TEXT NOT NULL,
    principal_id TEXT NOT NULL DEFAULT '',
    source_key TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'message',
    payload JSONB NOT NULL DEFAULT '{}',
    immutable_media JSONB NOT NULL DEFAULT '[]',
    payload_bytes BIGINT NOT NULL DEFAULT 0 CHECK (payload_bytes >= 0),
    attachment_bytes BIGINT NOT NULL DEFAULT 0 CHECK (attachment_bytes >= 0),
    expected_session_id TEXT,
    binding_revision BIGINT NOT NULL DEFAULT 0 CHECK (binding_revision >= 0),
    status TEXT NOT NULL DEFAULT 'pending',
    attempt_count BIGINT NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    claim_token UUID,
    claim_expires_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ,
    blocked_reason TEXT NOT NULL DEFAULT '',
    rejected_by TEXT NOT NULL DEFAULT '',
    rejected_at TIMESTAMPTZ,
    run_id UUID REFERENCES agent_run(id) ON DELETE RESTRICT,
    source_dispatch_id UUID REFERENCES ctx_group_dispatch(id) ON DELETE RESTRICT,
    source_responder_agent_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (channel_id, source_key),
    UNIQUE (channel_id, binding_key, binding_revision),
    CONSTRAINT channel_binding_fifo_payload_shape CHECK (
        payload_bytes = pg_column_size(payload::text) AND
        payload_bytes <= 33554432 AND
        attachment_bytes <= 62914560 AND
        jsonb_typeof(payload) = 'array' AND
        jsonb_typeof(immutable_media) = 'array'
    ),
    CONSTRAINT channel_binding_fifo_canonical_payload CHECK (
        channel_fifo_payload_is_canonical(payload)
    ),
    CONSTRAINT channel_binding_fifo_media_shape CHECK (
        channel_fifo_media_is_canonical(payload, immutable_media, attachment_bytes)
    ),
    CONSTRAINT channel_binding_fifo_claim_shape CHECK (
        (status = 'running' AND claim_token IS NOT NULL AND claim_expires_at IS NOT NULL)
        OR (status <> 'running' AND claim_token IS NULL AND claim_expires_at IS NULL)
    ),
    CONSTRAINT channel_binding_fifo_rejection_shape CHECK (
        (rejected_at IS NULL AND rejected_by = '')
        OR (status = 'rejected' AND rejected_at IS NOT NULL AND rejected_by <> '')
    ),
    CONSTRAINT channel_binding_fifo_group_source_shape CHECK (
        (source_dispatch_id IS NULL AND source_responder_agent_id IS NULL)
        OR (source_dispatch_id IS NOT NULL AND source_responder_agent_id IS NOT NULL)
    ),
    CONSTRAINT channel_binding_fifo_status_valid CHECK (
        status IN ('pending', 'running', 'blocked', 'completed', 'rejected')
    )
);
CREATE UNIQUE INDEX idx_channel_binding_fifo_group_source
    ON channel_binding_fifo(source_dispatch_id, source_responder_agent_id)
    WHERE source_dispatch_id IS NOT NULL;
CREATE INDEX idx_channel_binding_fifo_head
    ON channel_binding_fifo(channel_id, binding_key, enqueue_seq)
    WHERE status IN ('pending', 'blocked');
CREATE INDEX idx_channel_binding_fifo_run_id
    ON channel_binding_fifo(run_id)
    WHERE run_id IS NOT NULL;

-- Expiring reply routes (for example DingTalk session webhooks) are encrypted
-- under the deployment vault key. Durable envelopes carry only this opaque ID.
CREATE TABLE channel_reply_capability (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    channel_id TEXT NOT NULL REFERENCES channel(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL,
    ciphertext TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (kind IN ('dingtalk_session_webhook')),
    CHECK (expires_at > created_at)
);
CREATE INDEX idx_channel_reply_capability_expiry
    ON channel_reply_capability(expires_at);

-- Preserve the once-consumed identity of historical /new commands before the
-- FIFO becomes the ordered authority. Old receipts did not store the expected
-- Session, so they are terminal barriers and are never replayed.
INSERT INTO channel_binding_fifo (
    channel_id, binding_key, source_key, kind, payload, payload_bytes,
    binding_revision, status, created_at, updated_at
)
SELECT receipt.channel_id, receipt.binding,
       'command:' || receipt.chat_key || ':' || receipt.message_id,
       'new', '[]'::jsonb, pg_column_size('[]'::jsonb::text),
       row_number() OVER (PARTITION BY receipt.channel_id, receipt.binding ORDER BY receipt.created_at, receipt.message_id),
       'completed',
       receipt.created_at, receipt.updated_at
FROM channel_chat_command_receipt receipt
JOIN channel configured ON configured.id = receipt.channel_id
ON CONFLICT (channel_id, source_key) DO NOTHING;

-- GroupRoute classifies one group sequence. Its claim is not an execution
-- lease; accepted responders receive ordinary FIFO rows and later AgentRuns.
CREATE TABLE channel_group_route (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    group_message_id UUID NOT NULL UNIQUE REFERENCES ctx_group_message(id) ON DELETE CASCADE,
    group_id UUID NOT NULL REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    group_seq BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    claim_token UUID,
    claim_expires_at TIMESTAMPTZ,
    decisions JSONB NOT NULL DEFAULT '[]',
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (group_id, group_seq),
    CONSTRAINT channel_group_route_status_valid CHECK (
        status IN ('pending', 'claimed', 'completed')
    ),
    CONSTRAINT channel_group_route_claim_shape CHECK (
        (status = 'claimed' AND claim_token IS NOT NULL AND claim_expires_at IS NOT NULL)
        OR (status <> 'claimed' AND claim_token IS NULL AND claim_expires_at IS NULL)
    ),
    CONSTRAINT channel_group_route_completion_shape CHECK (
        (status = 'completed' AND completed_at IS NOT NULL)
        OR (status <> 'completed' AND completed_at IS NULL)
    )
);
CREATE INDEX idx_channel_group_route_pending
    ON channel_group_route(group_id, group_seq)
    WHERE status IN ('pending', 'claimed');

INSERT INTO channel_group_route (
    group_message_id, group_id, group_seq, status, completed_at, created_at, updated_at
)
SELECT outbox.group_message_id, outbox.group_id, message.seq,
       CASE WHEN outbox.status = 'completed' THEN 'completed' ELSE 'pending' END,
       CASE WHEN outbox.status = 'completed' THEN outbox.updated_at ELSE NULL END,
       outbox.created_at, outbox.updated_at
FROM ctx_group_outbox outbox
JOIN ctx_group_message message ON message.id = outbox.group_message_id
ON CONFLICT (group_message_id) DO NOTHING;

-- +goose Down
DROP TABLE channel_group_route;
DROP TABLE channel_reply_capability;
DROP TABLE channel_binding_fifo;
DROP FUNCTION channel_fifo_media_is_canonical(JSONB, JSONB, BIGINT);
DROP FUNCTION channel_fifo_payload_is_canonical(JSONB);
DROP INDEX idx_ctx_session_inbox_run_id;
ALTER TABLE ctx_session_inbox DROP COLUMN run_id;
DROP TABLE agent_session_sandbox;
DROP TABLE agent_run;
DROP TABLE runtime_executor_boot;
