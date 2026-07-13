-- +goose Up
-- Active knowledge is listed by its latest lifecycle write.
CREATE INDEX idx_facts_knowledge_active_updated_at
ON facts (user_id, agent_id, updated_at DESC, id DESC)
WHERE scope = 'user_agent' AND subject = 'world' AND status = 'active';

-- Lifecycle lookups need the newest deprecation audit record for a fact.
CREATE INDEX idx_ctx_agent_memory_changelog_fact_deprecate_latest
ON ctx_agent_memory_changelog (user_id, agent_id, entity_id, created_at DESC, id DESC)
WHERE scope = 'fact' AND action = 'deprecate';

-- +goose Down
DROP INDEX idx_ctx_agent_memory_changelog_fact_deprecate_latest;
DROP INDEX idx_facts_knowledge_active_updated_at;
