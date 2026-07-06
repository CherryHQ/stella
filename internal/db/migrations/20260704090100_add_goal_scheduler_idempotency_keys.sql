-- +goose Up
ALTER TABLE agent_goal ADD COLUMN idempotency_key TEXT;
CREATE UNIQUE INDEX idx_agent_goal_idem ON agent_goal (user_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

ALTER TABLE sched_job ADD COLUMN idempotency_key TEXT;
CREATE UNIQUE INDEX idx_sched_job_idem ON sched_job (user_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

-- +goose Down
DROP INDEX idx_sched_job_idem;
ALTER TABLE sched_job DROP COLUMN idempotency_key;

DROP INDEX idx_agent_goal_idem;
ALTER TABLE agent_goal DROP COLUMN idempotency_key;
