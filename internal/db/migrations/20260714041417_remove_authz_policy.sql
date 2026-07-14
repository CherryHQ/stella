-- +goose Up
-- Custom authorization policies were never a supported product surface. Refuse
-- to discard any active rule: an operator must remove or migrate it explicitly.
LOCK TABLE authz_policy IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM authz_policy WHERE status = 'active') THEN
        RAISE EXCEPTION 'cannot remove custom authorization policies while active authz_policy rows exist';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TABLE authz_policy;
DROP TABLE authz_policy_revision;

-- +goose Down
-- The custom rows cannot be restored, but recreate the empty schema so a local
-- binary rollback has the tables its old generated queries expect.
CREATE TABLE authz_policy_revision (
    id integer PRIMARY KEY DEFAULT 1,
    revision bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT authz_policy_revision_singleton CHECK (id = 1)
);
INSERT INTO authz_policy_revision (id, revision) VALUES (1, 0);

CREATE TABLE authz_policy (
    id text PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    resource_type text NOT NULL DEFAULT '',
    action text NOT NULL DEFAULT '',
    effect text NOT NULL,
    subjects jsonb NOT NULL DEFAULT '{}',
    attributes jsonb NOT NULL DEFAULT '{}',
    catalog_version bigint NOT NULL DEFAULT 0,
    status text NOT NULL DEFAULT 'quarantined',
    quarantine_reason text NOT NULL DEFAULT '',
    priority bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT authz_policy_effect_check CHECK (effect = ANY (ARRAY['allow'::text, 'deny'::text]))
);
CREATE INDEX idx_authz_policy_active ON authz_policy (resource_type) WHERE status = 'active';
