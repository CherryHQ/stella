-- +goose Up
CREATE TABLE agent_workflow (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    owner_kind TEXT NOT NULL,
    user_id UUID REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id TEXT REFERENCES agent(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version >= 1),
    workflow_key TEXT,
    intent TEXT NOT NULL DEFAULT '',
    acceptance_contract JSONB NOT NULL DEFAULT '{}',
    convergence_policy JSONB NOT NULL DEFAULT '{}',
    inputs JSONB NOT NULL DEFAULT '[]',
    payload_format TEXT NOT NULL DEFAULT 'frozen/v0',
    payload JSONB NOT NULL DEFAULT '{}',
    fully_frozen BOOLEAN NOT NULL DEFAULT false,
    source_goal_id TEXT REFERENCES agent_goal(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_workflow_owner_check CHECK (
        ((owner_kind = 'user') AND (user_id IS NOT NULL) AND (agent_id IS NULL))
        OR ((owner_kind = 'agent') AND (user_id IS NOT NULL) AND (agent_id IS NOT NULL))
        OR ((owner_kind = 'system') AND (user_id IS NULL) AND (agent_id IS NULL))
    ),
    CONSTRAINT agent_workflow_owner_name_version_key UNIQUE NULLS NOT DISTINCT (owner_kind, user_id, agent_id, name, version)
);
CREATE UNIQUE INDEX idx_agent_workflow_system_key ON agent_workflow (workflow_key) WHERE owner_kind = 'system' AND workflow_key IS NOT NULL;
CREATE INDEX idx_agent_workflow_owner ON agent_workflow (owner_kind, user_id, agent_id, name, version DESC);
CREATE INDEX idx_agent_workflow_source_goal ON agent_workflow (source_goal_id);

CREATE TABLE agent_workflow_run (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    workflow_id UUID NOT NULL REFERENCES agent_workflow(id) ON DELETE CASCADE,
    workflow_version INTEGER NOT NULL CHECK (workflow_version >= 1),
    idempotency_key TEXT NOT NULL,
    root_goal_id TEXT REFERENCES agent_goal(id) ON DELETE SET NULL,
    status TEXT NOT NULL CHECK (status = ANY (ARRAY['claimed'::text, 'materializing'::text, 'done'::text, 'failed'::text, 'skipped'::text])),
    inputs JSONB NOT NULL DEFAULT '{}',
    plan_hash TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_workflow_run_workflow_id_idempotency_key_key UNIQUE (workflow_id, idempotency_key)
);
CREATE INDEX idx_agent_workflow_run_workflow_created ON agent_workflow_run (workflow_id, created_at DESC);
CREATE INDEX idx_agent_workflow_run_root_goal ON agent_workflow_run (root_goal_id);

ALTER TABLE agent_goal
    ADD COLUMN workflow_id UUID REFERENCES agent_workflow(id) ON DELETE SET NULL,
    ADD COLUMN workflow_version INTEGER;
CREATE INDEX idx_agent_goal_workflow ON agent_goal (workflow_id) WHERE workflow_id IS NOT NULL;

ALTER TABLE sched_job
    ADD COLUMN dispatch_kind TEXT NOT NULL DEFAULT 'chat' CHECK (dispatch_kind = ANY (ARRAY['chat'::text, 'workflow'::text]));

ALTER TABLE sched_job_run
    ADD COLUMN root_goal_id TEXT;

-- +goose Down
ALTER TABLE sched_job_run DROP COLUMN root_goal_id;

ALTER TABLE sched_job DROP COLUMN dispatch_kind;

DROP INDEX idx_agent_goal_workflow;
ALTER TABLE agent_goal
    DROP COLUMN workflow_version,
    DROP COLUMN workflow_id;

DROP TABLE agent_workflow_run;
DROP TABLE agent_workflow;
