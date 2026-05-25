CREATE TABLE sched_jobs (
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
    payload TEXT NOT NULL DEFAULT '{}',
    session_mode TEXT NOT NULL DEFAULT 'reuse',
    enabled INTEGER NOT NULL DEFAULT 1,
    agent_id TEXT,
    user_id TEXT,
    org_id TEXT NOT NULL REFERENCES auth_organization(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    last_run_at TEXT,
    last_error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_sched_jobs_owner ON sched_jobs(owner_kind, plugin_id, job_key);
CREATE INDEX idx_sched_jobs_org_id ON sched_jobs(org_id);
