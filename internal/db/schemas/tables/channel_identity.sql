CREATE TABLE channel_identity (
    id          TEXT NOT NULL PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    platform    TEXT NOT NULL,
    external_id TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(platform, external_id)
);

CREATE INDEX idx_channel_identity_user_id ON channel_identity(user_id);

-- Deferred FK closing the auth_user <-> channel_identity cycle. Inline on
-- auth_user.notify_identity_id would force a forward reference that Atlas's
-- in-order PostgreSQL schema loader rejects.
ALTER TABLE auth_user
    ADD CONSTRAINT auth_user_notify_identity_id_fkey
    FOREIGN KEY (notify_identity_id) REFERENCES channel_identity(id) ON DELETE SET NULL;
