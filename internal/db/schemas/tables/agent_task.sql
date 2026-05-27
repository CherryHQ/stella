CREATE TABLE agent_task (
    id TEXT NOT NULL PRIMARY KEY,
    parent_id TEXT REFERENCES agent_task(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    task_type TEXT NOT NULL DEFAULT 'task' CHECK (task_type IN ('goal','task')),
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','ready','running','blocked','reviewing','changes_requested','done','failed','cancelled')),
    priority TEXT NOT NULL DEFAULT 'routine' CHECK (priority IN ('routine','urgent')),
    required BOOLEAN NOT NULL DEFAULT 1,
    retry_count INTEGER NOT NULL DEFAULT 0,
    max_retries INTEGER NOT NULL DEFAULT 3,
    review_policy TEXT CHECK (review_policy IN ('auto','agent','human')),
    session_id TEXT,
    context TEXT NOT NULL DEFAULT '{}',
    review_request TEXT NOT NULL DEFAULT '{}',
    notify_at TEXT,
    scheduler_job_id TEXT REFERENCES sched_job(id) ON DELETE SET NULL,
    scheduler_run_id TEXT REFERENCES sched_job_run(id) ON DELETE SET NULL,
    assignee_agent_id TEXT REFERENCES settings_agent(id) ON DELETE SET NULL,
    created_by_agent_id TEXT REFERENCES settings_agent(id) ON DELETE SET NULL,
    user_id TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK ((parent_id IS NULL) = (root_id = id))
);

CREATE INDEX idx_agent_task_user_id_status ON agent_task(user_id, status);
CREATE INDEX idx_agent_task_status ON agent_task(status);
CREATE INDEX idx_agent_task_session_id ON agent_task(session_id);
CREATE INDEX idx_agent_task_scheduler_job_id ON agent_task(scheduler_job_id);
CREATE INDEX idx_agent_task_scheduler_run_id ON agent_task(scheduler_run_id);
CREATE INDEX idx_agent_task_assignee_agent_id ON agent_task(assignee_agent_id);
CREATE INDEX idx_agent_task_root_id ON agent_task(root_id);
CREATE INDEX idx_agent_task_parent_id ON agent_task(parent_id);
CREATE INDEX idx_agent_task_type_status ON agent_task(task_type, status);
