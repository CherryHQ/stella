-- +goose Up
-- Session media is immutable evidence owned by exactly one session principal:
-- a user for a direct session, a group for a group session. owner_id is the
-- single column every read authorizes against, so a query never has to know
-- which of the two columns carries the principal.
--
-- A future product delete flow must purge the owner's blob prefix
-- (users/<user-id>/session-media, groups/<group-id>/session-media) before
-- deleting the owner and cascading this metadata; that cleanup is deferred for
-- both owner kinds.
ALTER TABLE ctx_media
    ALTER COLUMN user_id DROP NOT NULL;

ALTER TABLE ctx_media
    ADD COLUMN group_id UUID REFERENCES ctx_group_state(id) ON DELETE CASCADE;

ALTER TABLE ctx_media
    ADD COLUMN owner_id UUID GENERATED ALWAYS AS (COALESCE(user_id, group_id)) STORED;

ALTER TABLE ctx_media
    ADD CONSTRAINT ctx_media_owner_check CHECK (num_nonnulls(user_id, group_id) = 1);

ALTER TABLE ctx_media
    DROP CONSTRAINT ctx_media_user_id_sha256_key;

ALTER TABLE ctx_media
    ADD CONSTRAINT ctx_media_owner_id_sha256_key UNIQUE (owner_id, sha256);

-- +goose Down
ALTER TABLE ctx_media
    DROP CONSTRAINT ctx_media_owner_id_sha256_key;

DELETE FROM ctx_media WHERE group_id IS NOT NULL;

ALTER TABLE ctx_media
    DROP CONSTRAINT ctx_media_owner_check;

ALTER TABLE ctx_media
    DROP COLUMN owner_id;

ALTER TABLE ctx_media
    DROP COLUMN group_id;

ALTER TABLE ctx_media
    ALTER COLUMN user_id SET NOT NULL;

ALTER TABLE ctx_media
    ADD CONSTRAINT ctx_media_user_id_sha256_key UNIQUE (user_id, sha256);
