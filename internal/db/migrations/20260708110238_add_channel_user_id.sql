-- +goose Up
-- Bind a channel instance to a specific user. Only the inbound webhook channel
-- type populates this today: the webhook resolves its caller from a PAT and must
-- match the bound user. Other channel types leave it NULL (they resolve the user
-- from the inbound platform sender via channel_identity).
ALTER TABLE channel
    ADD COLUMN user_id UUID REFERENCES auth_user(id) ON DELETE CASCADE;
CREATE INDEX idx_channel_user_id ON channel (user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_channel_user_id;
ALTER TABLE channel DROP COLUMN IF EXISTS user_id;
