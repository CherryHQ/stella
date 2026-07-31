-- +goose Up
-- Schema for real `/new` session rotation in chats.
--
-- Two objects, one per property the rotation path depends on:
--
--   1. channel_chat_command_receipt — the once-per-message guard for a
--      destructive DM command.
--   2. idx_one_agent_group_chat — the one-active-chat-per-(agent, group)
--      invariant that binding-based session resolution rests on.
--
-- Both take locks behind live traffic, so bound the wait: a busy deployment
-- fails this migration and retries instead of queueing indefinitely (and
-- stalling every later statement behind its request). SET LOCAL scopes the
-- timeout to this migration's transaction.
SET LOCAL lock_timeout = '10s';

-- Records that one inbound private-chat message's slash command has already
-- been executed, so a platform redelivery answers instead of executing it a
-- second time. A DM has no event-log append to carry dedup for it the way an
-- ordinary group message gets it from ctx_group_message's unique index, so a
-- redelivered `/new` would re-resolve the successor session and rotate it
-- again — silently archiving everything said since.
--
-- The unique key is the message's PHYSICAL delivery coordinates: which channel
-- instance delivered it, which platform chat it arrived in, and its platform
-- message id. Routing state (which agent, which session binding) is
-- deliberately excluded: `/agent` or link changes alter routing, and the same
-- physical message redelivered after such a change is still the same message —
-- keying on routing would let it execute twice. For the same reason there is
-- no FK to agent: deleting an agent must not release receipts for messages
-- that already executed. `binding` is audit-only.
--
-- Claimed before the command runs, released only on provable non-execution,
-- and never expired: re-running a destructive reset would silently discard
-- whatever the chat said in between, while a lost reset is one message away
-- from being asked for again.
CREATE TABLE channel_chat_command_receipt (
    id         UUID PRIMARY KEY DEFAULT uuidv7(),
    -- Configured channel instance id (defaults to the platform name). Platform
    -- message ids are only unique within one channel instance — two bots on the
    -- same platform can both count message ids from 1 — so the instance, not
    -- the platform, is the id's namespace.
    channel_id TEXT NOT NULL,
    -- The physical chat the message arrived in: the platform chat id, or the
    -- sender's platform id for platforms that leave ChatID empty in DMs. One
    -- linked Stella user can own several platform accounts on one bot, and
    -- message ids are only unique per chat, so the chat must be part of the
    -- identity.
    chat_key   TEXT NOT NULL,
    -- Platform message id. Never empty: a message with no id cannot be
    -- recognised on redelivery, so the caller refuses to run the destructive
    -- command at all rather than storing a blank that would collapse every
    -- such message onto one row.
    message_id TEXT NOT NULL,
    command    TEXT NOT NULL,
    -- The chat binding the command executed under, for auditing only.
    binding    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (channel_id, chat_key, message_id)
);

-- One active chat session per (agent, group), the group counterpart of
-- idx_one_agent_main.
--
-- A group's binding is resolved on (agent, group) alone: the channel string
-- varies with the reply channel a message arrives through, so it cannot be part
-- of the predicate. That makes the in-process binding lock insufficient on its
-- own — two entry points (Web send and platform ingest), or two nodes, racing a
-- group's first message would each create an active row, and the binding's
-- newest-match lookup would silently strand one of them along with its history.
-- Group rows carry user_id = group_id, so (agent_id, user_id) is (agent, group).
--
-- The race this index closes could itself have left duplicates, and a unique
-- index over dirty data aborts the migration — which on an embedded-PostgreSQL
-- deployment means stellad refuses to start. Archive all but the newest active
-- row per binding first: the newest is what the binding's newest-match
-- resolution already answers with, so the losers were unreachable anyway and
-- their history stays intact. ctx_conversation holds one row per session (not
-- per message), so a plain in-transaction index build is fine here.
UPDATE ctx_conversation
SET archived = true, updated_at = now()
WHERE kind = 'chat' AND archived = false AND group_id IS NOT NULL
  AND session_id NOT IN (
    SELECT DISTINCT ON (agent_id, user_id) session_id
    FROM ctx_conversation
    WHERE kind = 'chat' AND archived = false AND group_id IS NOT NULL
    ORDER BY agent_id, user_id, last_active DESC, session_id DESC
  );

CREATE UNIQUE INDEX idx_one_agent_group_chat ON ctx_conversation (agent_id, user_id)
WHERE kind = 'chat' AND archived = false AND group_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_one_agent_group_chat;
DROP TABLE channel_chat_command_receipt;
