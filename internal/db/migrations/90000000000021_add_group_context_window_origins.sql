-- +goose Up
ALTER TABLE ctx_group_message
    ADD COLUMN actor_display_name TEXT NULL;

ALTER TABLE ctx_message
    ADD COLUMN origin_group_message_id UUID NULL REFERENCES ctx_group_message(id);

CREATE UNIQUE INDEX idx_ctx_message_conversation_origin_group_message
    ON ctx_message (conversation_id, origin_group_message_id)
    WHERE origin_group_message_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_ctx_message_conversation_origin_group_message;

ALTER TABLE ctx_message
    DROP COLUMN origin_group_message_id;

ALTER TABLE ctx_group_message
    DROP COLUMN actor_display_name;
