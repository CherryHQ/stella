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
CREATE UNIQUE INDEX idx_one_agent_group_chat ON ctx_conversation (agent_id, user_id)
WHERE kind = 'chat' AND archived = false AND group_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_one_agent_group_chat;
