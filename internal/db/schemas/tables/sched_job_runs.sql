CREATE TABLE sched_job_runs (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES sched_jobs(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'running',
    started_at TEXT NOT NULL DEFAULT (datetime('now')),
    finished_at TEXT,
    error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_sched_job_runs_job_id ON sched_job_runs(job_id, started_at DESC);
