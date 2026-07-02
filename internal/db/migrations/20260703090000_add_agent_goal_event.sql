-- +goose Up
CREATE TABLE agent_goal_event (
    id         UUID PRIMARY KEY DEFAULT uuidv7(),
    goal_id    TEXT NOT NULL REFERENCES agent_goal(id) ON DELETE CASCADE,
    attempt_id UUID REFERENCES agent_goal_attempt(id) ON DELETE SET NULL,
    event_type TEXT NOT NULL,
    payload    JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_agent_goal_event_goal_created ON agent_goal_event (goal_id, created_at);
CREATE INDEX idx_agent_goal_event_attempt ON agent_goal_event (attempt_id);

-- +goose Down
DROP TABLE agent_goal_event;
