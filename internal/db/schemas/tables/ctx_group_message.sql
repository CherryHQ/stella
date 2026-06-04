-- ctx_group_message is the authoritative, deduplicated log of every message seen
-- in a group. Several bots may deliver the same human message; the write path
-- collapses them into one row. Agents' own replies are written back here too, so
-- peer agents can read them as context.
CREATE TABLE ctx_group_message (
    id                  TEXT PRIMARY KEY,
    -- the group this message belongs to.
    group_id            TEXT NOT NULL REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    -- group-monotonic ordering token, allocated from ctx_group_state.next_seq.
    seq                 INTEGER NOT NULL,
    -- which bot/channel instance observed this delivery. Audit only: never part of
    -- a unique key (the same message arrives via several bots and must dedup to one
    -- row), and not a reply route (replies go out via the speaking agent's own bot).
    source_channel_id   TEXT,
    -- 'human' | 'agent'. Downstream branches on this hard fact, never guesses from
    -- content: the arbiter and memory ingest act on human rows only; agent rows are
    -- written back for context but do not wake the normal arbiter or ingest.
    actor_type          TEXT NOT NULL,
    -- who spoke: platform sender id when human, agent_id when agent.
    actor_id            TEXT NOT NULL,
    -- platform-native message id; nullable (some adapters cannot supply it).
    platform_message_id TEXT,
    -- platform message id this one replies to; empty/NULL if none.
    reply_to            TEXT,
    -- platform-reported send time (UTC); feeds the high-precision dedup fallback.
    platform_timestamp  TEXT,
    -- dedup fallback key, set only when there is no stable platform_message_id but a
    -- high-precision platform timestamp exists. Never derived from local receive time.
    idempotency_key     TEXT,
    -- JSON-serialized []ai.ContentBlock.
    content             TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (group_id, seq)
);

-- Primary dedup: one row per (group, platform message id) when the id is present.
CREATE UNIQUE INDEX idx_ctx_group_message_platform_msg
  ON ctx_group_message (group_id, platform_message_id)
  WHERE platform_message_id IS NOT NULL AND platform_message_id != '';

-- Fallback dedup for messages lacking a stable platform message id.
CREATE UNIQUE INDEX idx_ctx_group_message_idem
  ON ctx_group_message (idempotency_key)
  WHERE idempotency_key IS NOT NULL;
