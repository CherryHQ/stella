-- Per-criterion outcome inside a task review. Slice 2.

CREATE TABLE agent_review_item (
    id              TEXT NOT NULL PRIMARY KEY,
    review_id       TEXT NOT NULL REFERENCES agent_review(id) ON DELETE CASCADE,
    criterion_id    TEXT REFERENCES agent_task_criterion(id) ON DELETE SET NULL,
    passed          BOOLEAN,
    evidence        TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_agent_review_item_review ON agent_review_item(review_id);
