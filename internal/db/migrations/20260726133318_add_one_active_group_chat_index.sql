-- +goose Up
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
-- The cleanup UPDATE and the index build both take locks behind live traffic;
-- bound the wait so a busy deployment fails the migration and retries instead
-- of queueing indefinitely (and stalling every later DML behind its request).
-- SET LOCAL scopes the timeout to this migration's transaction.
SET LOCAL lock_timeout = '10s';

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
