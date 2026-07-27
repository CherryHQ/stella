-- +goose Up
-- Structured content blocks (JSON []ai.ContentBlock: text + image) for group
-- messages, so inbound images survive event-log storage and can be rehydrated
-- at dispatch. "content" remains the plain-text projection consumed by the
-- arbiter and history assembly; this column is written only when a message
-- carries non-text blocks and stays '[]' otherwise.
ALTER TABLE ctx_group_message ADD COLUMN content_blocks JSONB NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE ctx_group_message DROP COLUMN content_blocks;
