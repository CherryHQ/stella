-- +goose Up
-- Session media is immutable user-owned evidence. The object bytes live under
-- users/<user-id>/session-media in asset's private facet, outside sandbox roots.
-- Account deletion purges the per-user blob prefix *after* the delete commits,
-- not before: a purge before commit would destroy a live user's evidence on any
-- transaction that then rolled back. Blobs left behind by a crash in that window
-- are unreferenced, and the orphan sweeper collects them.
CREATE TABLE ctx_media (
    id          UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id     UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    sha256      BYTEA NOT NULL CHECK (octet_length(sha256) = 32),
    mime_type   TEXT NOT NULL,
    size_bytes  BIGINT NOT NULL CHECK (size_bytes > 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, sha256)
);

SET LOCAL lock_timeout = '5s';
ALTER TABLE ctx_message_part
    ADD COLUMN media_id UUID REFERENCES ctx_media(id) ON DELETE SET NULL,
    ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX idx_ctx_message_part_media_id ON ctx_message_part (media_id);

-- +goose Down
DROP INDEX idx_ctx_message_part_media_id;
ALTER TABLE ctx_message_part
    DROP COLUMN updated_at,
    DROP COLUMN created_at,
    DROP COLUMN media_id;
DROP TABLE ctx_media;
