-- +goose Up
-- OAuth2 authorization-server storage (issue #613, Phase 2 of #611). Stella acts
-- as the authorization/resource server issuing scoped, revocable tokens to
-- third-party clients on behalf of a user via authorization_code + PKCE and
-- refresh_token. Access tokens are OPAQUE stella_oat_ credentials resolved
-- through internal/credential (kind=oauth); they are NOT JWTs. OIDC id tokens /
-- JWKS signing keys are intentionally out of scope for this slice.

-- oauth_client is a registered third-party application owned by a user.
-- client_secret_hash is a bcrypt hash (client_secret is a password-like shared
-- secret, unlike the high-entropy opaque tokens hashed with SHA-256). Public
-- clients (client_type='public') have no secret and MUST use PKCE.
CREATE TABLE oauth_client (
    id                 UUID PRIMARY KEY DEFAULT uuidv7(),
    client_id          TEXT NOT NULL,
    name               TEXT NOT NULL DEFAULT '',
    client_secret_hash TEXT NOT NULL DEFAULT '',
    client_type        TEXT NOT NULL DEFAULT 'confidential',
    redirect_uris      TEXT[] NOT NULL DEFAULT '{}',
    grant_types        TEXT[] NOT NULL DEFAULT '{authorization_code,refresh_token}',
    scopes             TEXT[] NOT NULL DEFAULT '{}',
    owner_user_id      UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    disabled_at        TIMESTAMPTZ NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT oauth_client_client_id_key UNIQUE (client_id),
    CONSTRAINT oauth_client_type_check CHECK (client_type IN ('confidential', 'public'))
);
CREATE INDEX idx_oauth_client_owner ON oauth_client (owner_user_id);

-- oauth_authorization_code is a single-use, short-lived (~60s) code minted at
-- consent and exchanged at the token endpoint. code_hash is the SHA-256 of the
-- opaque code; the unique constraint plus consumed_at give single-use semantics
-- under a DB transaction (no in-memory flow state). code_challenge captures PKCE.
CREATE TABLE oauth_authorization_code (
    id                    UUID PRIMARY KEY DEFAULT uuidv7(),
    code_hash             TEXT NOT NULL,
    client_id             TEXT NOT NULL,
    user_id               UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    redirect_uri          TEXT NOT NULL,
    scopes                TEXT[] NOT NULL DEFAULT '{}',
    code_challenge        TEXT NOT NULL DEFAULT '',
    code_challenge_method TEXT NOT NULL DEFAULT '',
    expires_at            TIMESTAMPTZ NOT NULL,
    consumed_at           TIMESTAMPTZ NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT oauth_authorization_code_code_hash_key UNIQUE (code_hash)
);
CREATE INDEX idx_oauth_authorization_code_client ON oauth_authorization_code (client_id);
CREATE INDEX idx_oauth_authorization_code_user_client ON oauth_authorization_code (user_id, client_id);

-- oauth_refresh_family is the unit of revocation. A grant (one authorization_code
-- exchange) opens one family; every refresh token in the rotation chain and every
-- access token minted under it point at this row. Revocation -- reuse detection or
-- a user disconnecting the app -- sets revoked_at on this ONE row, and resolution
-- of both refresh and access tokens fails closed by joining here. This replaces a
-- fragile cascade of per-row UPDATEs with a single authoritative flag checked at
-- read time, so a token can never outlive its revoked family (issue #617 review).
CREATE TABLE oauth_refresh_family (
    id         UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id    UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    client_id  TEXT NOT NULL,
    revoked_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_oauth_refresh_family_user_client ON oauth_refresh_family (user_id, client_id);

-- oauth_refresh_token is the long-lived rotating credential (stella_ort_). Never
-- resolved at the API boundary -- only at /oauth/token. Rotation: a used token
-- gets consumed_at + replaced_by_id; presenting a consumed token (reuse) revokes
-- the whole family. token_hash is SHA-256 of the opaque secret. Revocation state
-- lives on oauth_refresh_family, not here.
CREATE TABLE oauth_refresh_token (
    id             UUID PRIMARY KEY DEFAULT uuidv7(),
    public_id      TEXT NOT NULL,
    token_hash     TEXT NOT NULL,
    client_id      TEXT NOT NULL,
    user_id        UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    scopes         TEXT[] NOT NULL DEFAULT '{}',
    family_id      UUID NOT NULL REFERENCES oauth_refresh_family(id) ON DELETE CASCADE,
    replaced_by_id UUID NULL,
    consumed_at    TIMESTAMPTZ NULL,
    expires_at     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT oauth_refresh_token_public_id_key UNIQUE (public_id)
);
CREATE INDEX idx_oauth_refresh_token_family ON oauth_refresh_token (family_id);
CREATE INDEX idx_oauth_refresh_token_user ON oauth_refresh_token (user_id);
CREATE INDEX idx_oauth_refresh_token_client ON oauth_refresh_token (client_id);

-- oauth_access_token is the opaque stella_oat_ bearer resolved through
-- internal/credential (kind=oauth). Mirrors personal_access_token: indexed
-- public_id lookup, SHA-256 token_hash, throttled last_used_at. refresh_family_id
-- is NOT NULL: every access token belongs to a refresh family and is revoked with
-- it (checked at resolve time via the family join, not a per-row revoked flag).
CREATE TABLE oauth_access_token (
    id                UUID PRIMARY KEY DEFAULT uuidv7(),
    public_id         TEXT NOT NULL,
    token_hash        TEXT NOT NULL,
    last4             TEXT NOT NULL DEFAULT '',
    client_id         TEXT NOT NULL,
    user_id           UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    scopes            TEXT[] NOT NULL DEFAULT '{}',
    refresh_family_id UUID NOT NULL REFERENCES oauth_refresh_family(id) ON DELETE CASCADE,
    expires_at        TIMESTAMPTZ NOT NULL,
    last_used_at      TIMESTAMPTZ NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT oauth_access_token_public_id_key UNIQUE (public_id)
);
CREATE INDEX idx_oauth_access_token_user ON oauth_access_token (user_id);
CREATE INDEX idx_oauth_access_token_client ON oauth_access_token (client_id);
CREATE INDEX idx_oauth_access_token_family ON oauth_access_token (refresh_family_id);

-- +goose Down
DROP TABLE oauth_access_token;
DROP TABLE oauth_refresh_token;
DROP TABLE oauth_refresh_family;
DROP TABLE oauth_authorization_code;
DROP TABLE oauth_client;
