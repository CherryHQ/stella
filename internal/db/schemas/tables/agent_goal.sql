-- Slice 3: high-level objective container.
-- A goal is a row, not a conversation (D12). Planning + synthesis happen as
-- runs in agent_task_run with kind='planner'/'synthesizer'; their sessions
-- live on the runs themselves.

CREATE TABLE agent_goal (
    id              TEXT NOT NULL PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id        TEXT REFERENCES agent(id) ON DELETE SET NULL,
    title           TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'draft',
    priority        TEXT NOT NULL DEFAULT 'routine',
    review_policy   TEXT NOT NULL DEFAULT 'none',
    active_review_id TEXT REFERENCES agent_review(id) ON DELETE RESTRICT,
    context         TEXT NOT NULL DEFAULT '{}',
    output          TEXT NOT NULL DEFAULT '{}',
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at    TEXT,
    cancelled_at    TEXT
);
