-- +goose Up
-- Session media is immutable evidence owned by exactly one session principal:
-- a user for a direct session, a group for a group session. (owner_kind,
-- owner_id) is the single pair every read authorizes against, so a query never
-- has to know which of the two columns carries the principal. The kind is part
-- of the identity because a user and a group are different principals even if
-- they ever carried the same UUID.
--
-- Deleting an owner purges its blob prefix (users/<user-id>/session-media,
-- groups/<group-id>/session-media) after the delete commits, so a rolled-back
-- transaction can never cost a live owner its evidence. A crash in that window
-- strands the prefix: the periodic sweep works from these rows, and the cascade
-- has already removed them.
--
-- That sweep exists for the other leak, where the row outlives its usefulness: a
-- group image is persisted before its message is appended, so a failed or
-- duplicate delivery leaves an unreferenced row and blob behind, and the sweep
-- deletes both once the row is older than the ingestion window.
SET LOCAL lock_timeout = '5s';

ALTER TABLE ctx_media
    ALTER COLUMN user_id DROP NOT NULL;

ALTER TABLE ctx_media
    ADD COLUMN group_id UUID REFERENCES ctx_group_state(id) ON DELETE CASCADE;

ALTER TABLE ctx_media
    ADD COLUMN owner_id UUID GENERATED ALWAYS AS (COALESCE(user_id, group_id)) STORED;

ALTER TABLE ctx_media
    ADD COLUMN owner_kind TEXT GENERATED ALWAYS AS (CASE WHEN user_id IS NOT NULL THEN 'user' ELSE 'group' END) STORED;

ALTER TABLE ctx_media
    ADD CONSTRAINT ctx_media_owner_check CHECK (num_nonnulls(user_id, group_id) = 1);

ALTER TABLE ctx_media
    DROP CONSTRAINT ctx_media_user_id_sha256_key;

ALTER TABLE ctx_media
    ADD CONSTRAINT ctx_media_owner_sha256_key UNIQUE (owner_kind, owner_id, sha256);

CREATE INDEX idx_ctx_media_user_id ON ctx_media (user_id);
CREATE INDEX idx_ctx_media_group_id ON ctx_media (group_id);

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP INDEX idx_ctx_media_group_id;
DROP INDEX idx_ctx_media_user_id;

ALTER TABLE ctx_media
    DROP CONSTRAINT ctx_media_owner_sha256_key;

DELETE FROM ctx_media WHERE group_id IS NOT NULL;

ALTER TABLE ctx_media
    DROP CONSTRAINT ctx_media_owner_check;

ALTER TABLE ctx_media
    DROP COLUMN owner_kind;

ALTER TABLE ctx_media
    DROP COLUMN owner_id;

ALTER TABLE ctx_media
    DROP COLUMN group_id;

ALTER TABLE ctx_media
    ALTER COLUMN user_id SET NOT NULL;

ALTER TABLE ctx_media
    ADD CONSTRAINT ctx_media_user_id_sha256_key UNIQUE (user_id, sha256);
