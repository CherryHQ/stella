-- +goose Up
-- A webhook channel is a personal resource, so ownership exists before its
-- capability endpoint is issued. Existing endpoint owners are preserved during
-- the cutover; older webhook rows without endpoints deliberately remain NULL.
ALTER TABLE channel
    ADD COLUMN owner_user_id UUID REFERENCES auth_user(id) ON DELETE RESTRICT,
    ADD CONSTRAINT channel_owner_webhook_only
        CHECK (owner_user_id IS NULL OR type = 'webhook');

UPDATE channel AS channel
SET owner_user_id = endpoint.owner_user_id
FROM channel_webhook_endpoint AS endpoint
WHERE endpoint.channel_id = channel.id;

CREATE INDEX idx_channel_owner_user_id ON channel (owner_user_id);

DROP INDEX idx_channel_webhook_endpoint_owner_user_id;
ALTER TABLE channel_webhook_endpoint DROP COLUMN owner_user_id;

-- +goose Down
ALTER TABLE channel_webhook_endpoint
    ADD COLUMN owner_user_id UUID REFERENCES auth_user(id) ON DELETE RESTRICT;

UPDATE channel_webhook_endpoint AS endpoint
SET owner_user_id = channel.owner_user_id
FROM channel
WHERE channel.id = endpoint.channel_id
  AND channel.owner_user_id IS NOT NULL;

ALTER TABLE channel_webhook_endpoint
    ALTER COLUMN owner_user_id SET NOT NULL;
CREATE INDEX idx_channel_webhook_endpoint_owner_user_id
    ON channel_webhook_endpoint (owner_user_id);

DROP INDEX idx_channel_owner_user_id;
ALTER TABLE channel
    DROP CONSTRAINT channel_owner_webhook_only,
    DROP COLUMN owner_user_id;
