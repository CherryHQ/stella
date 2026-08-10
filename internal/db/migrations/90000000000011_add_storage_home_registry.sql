-- +goose Up
-- Home owner identity fields deliberately have no foreign keys. A destructive owner
-- delete must tombstone the Home first, while the registry permanently retains
-- the original identity and purge audit after the owner row is gone.
CREATE TABLE storage_home (
    id                  UUID PRIMARY KEY DEFAULT uuidv7(),
    home_kind           TEXT NOT NULL,
    principal_kind      TEXT,
    principal_id        TEXT,
    agent_id            TEXT,
    store_id            TEXT NOT NULL,
    locator             TEXT NOT NULL,
    UNIQUE (store_id, locator),
    state               TEXT NOT NULL DEFAULT 'provisioning',
    maintenance_owner   TEXT,
    maintenance_until   TIMESTAMPTZ,
    tombstoned_at       TIMESTAMPTZ,
    tombstoned_by       TEXT,
    purge_attempts      INTEGER NOT NULL DEFAULT 0 CHECK (purge_attempts >= 0),
    purge_requested_at  TIMESTAMPTZ,
    purge_started_at    TIMESTAMPTZ,
    purge_failed_at     TIMESTAMPTZ,
    last_purge_error    TEXT,
    purged_at           TIMESTAMPTZ,
    purged_by           TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (home_kind = 'principal'
            AND principal_kind IS NOT NULL AND principal_id IS NOT NULL AND agent_id IS NULL)
        OR (home_kind = 'agent'
            AND principal_kind IS NOT NULL AND principal_id IS NOT NULL AND agent_id IS NOT NULL)
        OR (home_kind = 'system_skill'
            AND principal_kind IS NULL AND principal_id IS NULL AND agent_id IS NULL)
        OR (home_kind = 'system_agent_skill'
            AND principal_kind IS NULL AND principal_id IS NULL AND agent_id IS NOT NULL)
    ),
    CHECK (
        (maintenance_owner IS NULL AND maintenance_until IS NULL)
        OR (maintenance_owner IS NOT NULL AND maintenance_until IS NOT NULL)
    )
);

-- These indexes intentionally include purged rows: Home identity and locator
-- are permanent audit records and must never be reused.
CREATE UNIQUE INDEX idx_storage_home_principal_identity
    ON storage_home (principal_kind, principal_id)
    WHERE home_kind = 'principal';
CREATE UNIQUE INDEX idx_storage_home_agent_identity
    ON storage_home (principal_kind, principal_id, agent_id)
    WHERE home_kind = 'agent';
CREATE UNIQUE INDEX idx_storage_home_system_skill_singleton
    ON storage_home (home_kind)
    WHERE home_kind = 'system_skill';
CREATE UNIQUE INDEX idx_storage_home_system_agent_skill_agent
    ON storage_home (agent_id)
    WHERE home_kind = 'system_agent_skill';

-- Narrow durable markers for offline storage authority migrations. Metadata is
-- intentionally non-secret; Phase 1 only records configuration observation.
CREATE TABLE storage_migration (
    name                        TEXT PRIMARY KEY,
    state                       TEXT NOT NULL,
    object_authority_configured BOOLEAN NOT NULL DEFAULT false,
    metadata                    JSONB NOT NULL DEFAULT '{}',
    completed_at                TIMESTAMPTZ,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE storage_migration;
DROP TABLE storage_home;
