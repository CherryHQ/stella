-- +goose Up
-- Durable channel pipeline: multi-replica channel ownership, a durable
-- ingress/egress ledger, the agent_run unit of work, and the cross-replica
-- session event log.

-- Multi-replica channel ownership: a live `channel` row already carries the
-- configured intent; these columns record which replica currently owns the
-- poller, the ingress cursor it reached, and the last observed state.
ALTER TABLE "channel"
    -- Replica claiming the right to run this channel's poller. TEXT, not a
    -- FK: replicas are ephemeral processes, not database entities.
    ADD COLUMN runtime_owner_id TEXT,
    -- Fencing token pairing with runtime_lease_until. An owner may only act
    -- (poll, send, write checkpoint) while both are set and the lease is
    -- fresh; a new owner overwriting both implicitly fences the old one.
    ADD COLUMN runtime_token UUID,
    ADD COLUMN runtime_lease_until TIMESTAMPTZ,
    -- Owner-managed durable ingress cursor (platform checkpoints, update
    -- offsets). Read-only to anyone but the token holder.
    ADD COLUMN receive_checkpoint JSONB NOT NULL DEFAULT '{}',
    -- Bumped by every config change. Owners compare config_revision with
    -- applied_revision to detect that they run stale config.
    ADD COLUMN config_revision BIGINT NOT NULL DEFAULT 0,
    -- The revision the current owner last loaded.
    ADD COLUMN applied_revision BIGINT,
    -- Last state the owner reported: stopped/starting/running/draining/error.
    -- Valid values enforced in Go.
    ADD COLUMN runtime_state TEXT NOT NULL DEFAULT 'stopped',
    ADD COLUMN runtime_error_code TEXT,
    ADD COLUMN runtime_observed_at TIMESTAMPTZ,
    -- The platform account identity the current lease owner's adapter
    -- registered for this channel. Reply outbox ops snapshot this key at
    -- dispatch-accept time so the send-time account fence follows the
    -- responding channel's account, not the observing trigger's. Kept after
    -- lease release: a stale last-known key rejects pending ops under a
    -- re-bound account, which is the safe direction. Overwritten by the next
    -- owner's registration.
    ADD COLUMN runtime_account_key TEXT;

ALTER TABLE "channel"
    ADD CONSTRAINT channel_runtime_token_lease_pair
        CHECK ((runtime_token IS NULL) = (runtime_lease_until IS NULL)),
    ADD CONSTRAINT channel_revisions_nonnegative
        CHECK (config_revision >= 0 AND (applied_revision IS NULL OR applied_revision >= 0));

-- Replicas scan for channels whose lease expired to take over polling.
CREATE INDEX idx_channel_runtime_lease_until ON "channel" (runtime_lease_until)
    WHERE runtime_lease_until IS NOT NULL;

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

-- Durable unit of accepted agent work. A run exists once input is committed;
-- workers claim it under the session execution lease. State transitions are
-- one-way toward terminal states (completed/failed/canceled/interrupted) and
-- enforced in the store layer.
CREATE TABLE agent_run (
    id             UUID PRIMARY KEY DEFAULT uuidv7(),
    -- Ingress event that produced this run. NULL for non-channel input (Web).
    -- SET NULL so audit retention can eventually detach without breaking
    -- session deletion.
    inbox_id       UUID REFERENCES channel_inbox(id) ON DELETE SET NULL,
    session_id     TEXT NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE CASCADE,
    agent_id       TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    -- End-to-end idempotency key, namespaced by the caller's source
    -- (e.g. "inbox:<uuid>" for channel input). One logical request mints at
    -- most one run; retries attach, never duplicate.
    request_key    TEXT NOT NULL,
    UNIQUE (request_key),
    -- enqueue_seq is allocated under the session row lock; this constraint is
    -- the backstop that turns a missed lock into an error, not a wrong order.
    UNIQUE (session_id, enqueue_seq),
    -- Versioned value objects: who asked, what was asked, where the final
    -- answer goes. Snapshots, so later config changes cannot rewrite history.
    actor          JSONB NOT NULL DEFAULT '{}',
    input          JSONB NOT NULL DEFAULT '{}',
    reply_address  JSONB NOT NULL DEFAULT '{}',
    -- Queue order within the session, assigned under the session row lock so
    -- a later request can never overtake an earlier non-terminal run.
    enqueue_seq    BIGINT NOT NULL CHECK (enqueue_seq >= 0),
    -- queued / running / completed / failed / canceled / interrupted. In Go.
    state          TEXT NOT NULL DEFAULT 'queued',
    -- Worker replica currently executing, for observability only; authority
    -- comes from ctx_session_execution.
    worker_id      TEXT,
    error_code     TEXT,
    -- Predecessor when this run is a bounded recovery retry.
    retry_of_run_id UUID REFERENCES agent_run(id) ON DELETE SET NULL,
    started_at     TIMESTAMPTZ,
    finished_at    TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (retry_of_run_id IS NULL OR retry_of_run_id != id)
);

-- Worker scan: oldest queued runs first, FOR UPDATE SKIP LOCKED.
CREATE INDEX idx_agent_run_queued ON agent_run (enqueue_seq)
    WHERE state = 'queued';
-- Per-session non-terminal ordering.
CREATE INDEX idx_agent_run_session_open ON agent_run (session_id, enqueue_seq)
    WHERE state IN ('queued', 'running');
-- At most one running run per session.
CREATE UNIQUE INDEX idx_agent_run_one_running_per_session ON agent_run (session_id)
    WHERE state = 'running';
CREATE INDEX idx_agent_run_inbox ON agent_run (inbox_id) WHERE inbox_id IS NOT NULL;
CREATE INDEX idx_agent_run_retry_of ON agent_run (retry_of_run_id) WHERE retry_of_run_id IS NOT NULL;

ALTER TABLE ctx_session_execution
    -- The execution lease is the single-writer fence for a session; linking it
    -- to its run lets a reaper and the completion transaction agree on which
    -- run the token covers. SET NULL keeps the lease usable if its run row is
    -- removed.
    ADD COLUMN run_id UUID REFERENCES agent_run(id) ON DELETE SET NULL,
    -- Writer-exit evidence for session takeover: a claimant may only overwrite
    -- an expired lease when the previous owner is confirmed dead or the row
    -- is owner-less. An alive or unverifiable owner keeps the session
    -- unavailable rather than letting two writers share one workspace
    -- (plan D9).
    ADD COLUMN owner_id TEXT,
    ADD COLUMN owner_host TEXT,
    ADD COLUMN owner_pid INTEGER;

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
    -- Group replies are not produced by an agent_run, so run_id cannot anchor
    -- their lifecycle. Pin them to the group itself: deleting the group drops
    -- its pending sends, while deleting a channel or dispatch row must not
    -- erase delivery history.
    group_id           UUID REFERENCES ctx_group_state(id) ON DELETE CASCADE,
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
CREATE INDEX idx_channel_outbox_group ON channel_outbox (group_id) WHERE group_id IS NOT NULL;

-- Reply file attachments live in a bytea child of the outbox op rather than in
-- the payload JSONB: ListPendingChannelOutbox batches SELECT * on the parent
-- table and must not haul megabytes of file data per row; the attachment is
-- read lazily by the op that owns it. The trade-off is database size — WAL and
-- backups carry the bytes — so an external blob store is the upgrade only when
-- a real budget or lease-duration measurement says so.
CREATE TABLE channel_outbox_attachment (
    -- One attachment body per send_attachment op.
    outbox_id  UUID PRIMARY KEY REFERENCES channel_outbox(id) ON DELETE CASCADE,
    data       BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Durable session event log: every live turn event is also appended here with
-- a session-scoped sequence, so a watcher on any replica can replay or tail a
-- turn the local SessionHub never saw. The in-memory hub stays as a fast path;
-- this log is the cross-process truth and the reconnect cursor.
CREATE TABLE ctx_session_event (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id TEXT        NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE CASCADE,
    run_id     UUID        REFERENCES agent_run(id) ON DELETE SET NULL,
    -- Per-session contiguous sequence; assigned under the session advisory lock.
    seq        BIGINT      NOT NULL,
    -- Execution coordinate for the durable event log: events produced under a
    -- session execution lease carry its token so observers can tail turns
    -- that never had an agent_run (scheduler, delegate, session.send). The
    -- column is deliberately reference-free — the execution row is deleted
    -- on finish, and the marker/event rows must outlive it.
    execution_id UUID,
    -- JSON of the serializable event subset (text/reasoning/tool_use/step/
    -- references/image/file/error). Transport-internal fields (Store) are not
    -- stored — history rows already carry them.
    event      JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Replay/tail scan: WHERE session_id=$ AND seq>$ ORDER BY seq.
ALTER TABLE ctx_session_event
    ADD CONSTRAINT ctx_session_event_session_seq UNIQUE (session_id, seq);
CREATE INDEX idx_ctx_session_event_session_seq ON ctx_session_event (session_id, seq);
-- Tail-by-execution scan: WHERE session_id=$ AND execution_id=$ AND seq>$.
CREATE INDEX idx_ctx_session_event_execution
    ON ctx_session_event (session_id, execution_id, seq)
    WHERE execution_id IS NOT NULL;
-- Retention sweep by age.
CREATE INDEX idx_ctx_session_event_created ON ctx_session_event (created_at);

-- Monotonic per-session event counter. ctx_session_event rows are pruned by
-- retention, so MAX(seq)+1 would reset; this counter lives on the session row
-- and never shrinks, keeping reconnect cursors unambiguous across prunes.
ALTER TABLE ctx_conversation ADD COLUMN event_seq BIGINT NOT NULL DEFAULT 0;

-- The bot account that received a platform group message. Audit-only, like
-- source_channel_id: a shared trigger can wake members replying through other
-- channels, so reply ops fence on the reply channel's account snapshot
-- (channel.runtime_account_key), not this observing account.
ALTER TABLE ctx_group_message
    ADD COLUMN source_account_key TEXT;

-- +goose Down
ALTER TABLE ctx_group_message
    DROP COLUMN source_account_key;

ALTER TABLE ctx_conversation DROP COLUMN event_seq;

DROP TABLE ctx_session_event;

DROP TABLE channel_outbox_attachment;
DROP TABLE channel_outbox;

ALTER TABLE ctx_session_execution
    DROP COLUMN run_id,
    DROP COLUMN owner_id,
    DROP COLUMN owner_host,
    DROP COLUMN owner_pid;

DROP TABLE agent_run;
DROP TABLE channel_inbox;

ALTER TABLE "channel"
    DROP CONSTRAINT channel_runtime_token_lease_pair,
    DROP CONSTRAINT channel_revisions_nonnegative;

DROP INDEX idx_channel_runtime_lease_until;

ALTER TABLE "channel"
    DROP COLUMN runtime_owner_id,
    DROP COLUMN runtime_token,
    DROP COLUMN runtime_lease_until,
    DROP COLUMN receive_checkpoint,
    DROP COLUMN config_revision,
    DROP COLUMN applied_revision,
    DROP COLUMN runtime_state,
    DROP COLUMN runtime_error_code,
    DROP COLUMN runtime_observed_at,
    DROP COLUMN runtime_account_key;
