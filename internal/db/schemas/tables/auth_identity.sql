CREATE TABLE auth_identity (
    id               TEXT NOT NULL PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    provider         TEXT NOT NULL,
    provider_subject TEXT NOT NULL,
    email            TEXT NOT NULL DEFAULT '',
    name             TEXT NOT NULL DEFAULT '',
    avatar_url       TEXT NOT NULL DEFAULT '',
    raw_claims       JSONB NOT NULL DEFAULT '{}',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(provider, provider_subject)
);

CREATE INDEX idx_auth_identity_user_id ON auth_identity(user_id);
