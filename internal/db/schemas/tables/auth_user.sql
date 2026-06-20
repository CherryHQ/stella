CREATE TABLE auth_user (
    id                  UUID NOT NULL PRIMARY KEY DEFAULT uuidv7(),
    email               TEXT NOT NULL UNIQUE,
    name                TEXT NOT NULL DEFAULT '',
    avatar_url          TEXT NOT NULL DEFAULT '',
    role                TEXT NOT NULL DEFAULT 'user',
    is_active           BOOLEAN NOT NULL DEFAULT true,
    default_agent_id    TEXT REFERENCES agent(id),
    notify_identity_id  UUID,  -- FK added in channel_identity.sql (cycle: auth_user <-> channel_identity)
    age_public_key      TEXT NOT NULL DEFAULT '',
    age_private_key     TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_auth_user_email ON auth_user(email);
