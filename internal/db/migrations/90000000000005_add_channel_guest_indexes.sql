-- +goose NO TRANSACTION

-- +goose Up
-- These indexes scan the populated conversation table. Build them without
-- blocking conversation writes, and clean up an invalid prior attempt before
-- retrying this unapplied migration.
SET lock_timeout = '10s';

DROP INDEX CONCURRENTLY IF EXISTS idx_ctx_conversation_guest_id;
CREATE INDEX CONCURRENTLY idx_ctx_conversation_guest_id
    ON ctx_conversation(guest_id)
    WHERE guest_id IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS idx_one_agent_guest_chat;
CREATE UNIQUE INDEX CONCURRENTLY idx_one_agent_guest_chat
    ON ctx_conversation(agent_id, guest_id)
    WHERE kind = 'chat' AND archived = false AND guest_id IS NOT NULL;

RESET lock_timeout;

-- +goose Down
SET lock_timeout = '10s';
DROP INDEX CONCURRENTLY IF EXISTS idx_one_agent_guest_chat;
DROP INDEX CONCURRENTLY IF EXISTS idx_ctx_conversation_guest_id;
RESET lock_timeout;
