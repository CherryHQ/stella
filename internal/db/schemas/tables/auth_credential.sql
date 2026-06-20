CREATE TABLE auth_credential (
    id           UUID NOT NULL PRIMARY KEY DEFAULT uuidv7(),
    user_id      UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    password_hash TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(user_id)
);

CREATE INDEX idx_auth_credential_user_id ON auth_credential(user_id);
