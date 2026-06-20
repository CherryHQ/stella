CREATE TABLE sched_job_run (
    id TEXT NOT NULL PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES sched_job(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'running',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    error TEXT NOT NULL DEFAULT '',
    output TEXT NOT NULL DEFAULT '',
    user_id TEXT
);

CREATE INDEX idx_sched_job_run_job_id ON sched_job_run(job_id, started_at DESC);
