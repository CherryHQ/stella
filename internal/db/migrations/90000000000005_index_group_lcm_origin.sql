-- +goose NO TRANSACTION

-- +goose Up
SET lock_timeout = '5s';

-- Remove an invalid index left by an interrupted concurrent build before retrying.
DROP INDEX CONCURRENTLY IF EXISTS "idx_ctx_message_conversation_group_origin";
CREATE UNIQUE INDEX CONCURRENTLY "idx_ctx_message_conversation_group_origin"
ON "ctx_message" ("conversation_id", "origin_group_message_id")
WHERE "origin_group_message_id" IS NOT NULL;

RESET lock_timeout;

-- +goose Down
SET lock_timeout = '5s';
DROP INDEX CONCURRENTLY IF EXISTS "idx_ctx_message_conversation_group_origin";
RESET lock_timeout;
