-- +goose NO TRANSACTION

-- +goose Up
-- ctx_message is populated in production. Build the idempotency fence without
-- blocking transcript writes, and remove an invalid prior attempt before retry.
SET lock_timeout = '10s';
DROP INDEX CONCURRENTLY IF EXISTS idx_ctx_message_inbox_id;
CREATE UNIQUE INDEX CONCURRENTLY idx_ctx_message_inbox_id
    ON ctx_message(inbox_id)
    WHERE inbox_id IS NOT NULL;
RESET lock_timeout;

-- +goose Down
SET lock_timeout = '10s';
DROP INDEX CONCURRENTLY IF EXISTS idx_ctx_message_inbox_id;
RESET lock_timeout;
