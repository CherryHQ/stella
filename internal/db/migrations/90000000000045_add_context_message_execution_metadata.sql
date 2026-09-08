-- +goose Up
ALTER TABLE ctx_message
    ADD COLUMN execution_metadata JSONB,
    ADD CONSTRAINT ctx_message_execution_metadata_object
        CHECK (execution_metadata IS NULL OR jsonb_typeof(execution_metadata) = 'object');

-- +goose Down
ALTER TABLE ctx_message
    DROP CONSTRAINT ctx_message_execution_metadata_object,
    DROP COLUMN execution_metadata;
