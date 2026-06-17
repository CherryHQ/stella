-- Per-task acceptance criteria, evaluated by reviewers. Slice 2.

CREATE TABLE agent_task_criterion (
    id              TEXT NOT NULL PRIMARY KEY,
    task_id         TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    description     TEXT NOT NULL,
    required_flag   BOOLEAN NOT NULL DEFAULT true,
    position        BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_agent_task_criterion_task ON agent_task_criterion(task_id, position);
