CREATE TABLE sched_job (
    id TEXT PRIMARY KEY,
    owner_kind TEXT NOT NULL DEFAULT 'user',
    exec_scope TEXT NOT NULL DEFAULT 'user',
    plugin_id TEXT NOT NULL DEFAULT '',
    job_key TEXT NOT NULL DEFAULT '',
    runtime_name TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    schedule_cron TEXT NOT NULL DEFAULT '',
    schedule_every TEXT NOT NULL DEFAULT '',
    schedule_at TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL DEFAULT '{}',
    session_mode TEXT NOT NULL DEFAULT 'reuse',
    enabled BOOLEAN NOT NULL DEFAULT true,
    agent_id TEXT,
    user_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_run_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_sched_job_owner ON sched_job(owner_kind, plugin_id, job_key);
