-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE auth_provisioned_user (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    external_id TEXT NOT NULL UNIQUE,
    user_id UUID NOT NULL UNIQUE REFERENCES auth_user(id) ON DELETE CASCADE,
    created_by_user_id UUID REFERENCES auth_user(id) ON DELETE SET NULL,
    created_by_token_id UUID REFERENCES personal_access_token(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- user_id's UNIQUE constraint supplies its foreign-key index.
CREATE INDEX idx_auth_provisioned_user_created_by_user_id
    ON auth_provisioned_user (created_by_user_id);
CREATE INDEX idx_auth_provisioned_user_created_by_token_id
    ON auth_provisioned_user (created_by_token_id);
CREATE INDEX idx_auth_provisioned_user_created_at_id
    ON auth_provisioned_user (created_at ASC, id ASC);

ALTER TABLE personal_access_token
    ADD COLUMN issued_by_token_id UUID REFERENCES personal_access_token(id) ON DELETE SET NULL,
    ADD COLUMN issued_by_provisioning BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX idx_personal_access_token_issued_by_token_id
    ON personal_access_token (issued_by_token_id);

-- A provisioning request may be retried after its one-time response is lost.
-- This backstop permits exactly one non-revoked provisioned PAT per target even
-- when concurrent callers bypass the service's row-lock protocol.
CREATE UNIQUE INDEX idx_personal_access_token_active_provisioned_user
    ON personal_access_token (user_id)
    WHERE issued_by_provisioning AND revoked_at IS NULL;

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP INDEX idx_personal_access_token_active_provisioned_user;
DROP INDEX idx_personal_access_token_issued_by_token_id;
ALTER TABLE personal_access_token
    DROP COLUMN issued_by_token_id,
    DROP COLUMN issued_by_provisioning;
DROP TABLE auth_provisioned_user;
