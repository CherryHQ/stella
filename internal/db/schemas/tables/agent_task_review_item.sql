CREATE TABLE agent_task_review_item (
    id TEXT NOT NULL PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    review_id TEXT NOT NULL REFERENCES agent_task_review(id) ON DELETE CASCADE,
    criterion_id TEXT NOT NULL REFERENCES agent_task_acceptance_criterion(id) ON DELETE CASCADE,
    passed BOOLEAN,
    evidence TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(review_id, criterion_id)
);

CREATE INDEX idx_agent_task_review_item_review_id ON agent_task_review_item(review_id);
