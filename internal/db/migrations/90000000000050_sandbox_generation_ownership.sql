-- +goose Up
SET LOCAL lock_timeout = '10s';

-- A Session's compute is disposable.  This row is the authority that says
-- which executor created one generation and whether a replacement is allowed.
-- It deliberately contains no workspace locator: generation identifies compute
-- only, while durable files remain owned by WorkspaceManager/POSIX roots.
CREATE TABLE agent_sandbox_generation (
    session_id TEXT NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE RESTRICT,
    generation BIGINT NOT NULL,
    owner_boot_id UUID NOT NULL REFERENCES runtime_executor_boot(id) ON DELETE RESTRICT,
    backend TEXT NOT NULL,
    config_digest TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'creating',
    resource_authority TEXT NOT NULL DEFAULT '',
    resource_ref TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    active_at TIMESTAMPTZ,
    fenced_at TIMESTAMPTZ,
    destroyed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_sandbox_generation_pk PRIMARY KEY (session_id, generation),
    CONSTRAINT agent_sandbox_generation_positive CHECK (generation > 0),
    CONSTRAINT agent_sandbox_generation_backend_present CHECK (backend <> ''),
    CONSTRAINT agent_sandbox_generation_digest_present CHECK (config_digest <> ''),
    CONSTRAINT agent_sandbox_generation_state_valid CHECK (
        state IN ('creating', 'active', 'fenced', 'unknown', 'destroyed')
    ),
    CONSTRAINT agent_sandbox_generation_state_shape CHECK (
        (state = 'creating' AND active_at IS NULL AND fenced_at IS NULL AND destroyed_at IS NULL)
        OR (state = 'active' AND active_at IS NOT NULL AND fenced_at IS NULL AND destroyed_at IS NULL)
        OR (state IN ('fenced', 'unknown') AND fenced_at IS NOT NULL AND destroyed_at IS NULL)
        OR (state = 'destroyed' AND destroyed_at IS NOT NULL)
    )
);

-- At most one generation may still own compute for a Session.  Destroyed rows
-- remain as monotonic history and are the only rows eligible for retention.
CREATE UNIQUE INDEX idx_agent_sandbox_generation_one_live
    ON agent_sandbox_generation(session_id)
    WHERE state <> 'destroyed';
CREATE INDEX idx_agent_sandbox_generation_boot_state
    ON agent_sandbox_generation(owner_boot_id, state);
CREATE INDEX idx_agent_sandbox_generation_updated
    ON agent_sandbox_generation(updated_at);

-- Preparation resources (for example a private native CLI install session)
-- belong to the generation but have a lifecycle independent from its main
-- compute resource. The backing root is an audit/cleanup coordinate only; it
-- is never a workspace locator and is never exposed to the agent.
CREATE TABLE agent_sandbox_generation_auxiliary (
    aux_id UUID PRIMARY KEY DEFAULT uuidv7(),
    session_id TEXT NOT NULL,
    generation BIGINT NOT NULL,
    role TEXT NOT NULL DEFAULT 'prep',
    backend TEXT NOT NULL,
    backing_root TEXT NOT NULL DEFAULT '',
    resource_authority TEXT NOT NULL DEFAULT '',
    resource_ref TEXT NOT NULL DEFAULT '',
    execution_state TEXT NOT NULL DEFAULT 'creating',
    termination_state TEXT NOT NULL DEFAULT 'open',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_sandbox_generation_auxiliary_generation_fk
        FOREIGN KEY (session_id, generation)
        REFERENCES agent_sandbox_generation(session_id, generation)
        ON DELETE RESTRICT
);
CREATE INDEX idx_agent_sandbox_generation_auxiliary_generation
    ON agent_sandbox_generation_auxiliary(session_id, generation, termination_state);

-- +goose Down
DROP INDEX idx_agent_sandbox_generation_auxiliary_generation;
DROP TABLE agent_sandbox_generation_auxiliary;
DROP INDEX idx_agent_sandbox_generation_updated;
DROP INDEX idx_agent_sandbox_generation_boot_state;
DROP INDEX idx_agent_sandbox_generation_one_live;
DROP TABLE agent_sandbox_generation;
