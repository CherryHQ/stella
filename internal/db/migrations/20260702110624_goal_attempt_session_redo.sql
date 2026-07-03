-- +goose Up
ALTER TABLE agent_goal_attempt
    ADD COLUMN repair_rounds integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT agent_goal_attempt_repair_rounds_check CHECK (repair_rounds >= 0);

ALTER TABLE agent_goal
    ADD COLUMN flaky_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN budget_bonus integer NOT NULL DEFAULT 0,
    ADD COLUMN done_reason text NOT NULL DEFAULT '',
    ADD CONSTRAINT agent_goal_flaky_count_check CHECK (flaky_count >= 0);

ALTER TABLE agent_goal_attempt
    ADD COLUMN previous_failure_class text NOT NULL DEFAULT '';

COMMENT ON COLUMN agent_goal.flaky_count IS 'Infrastructure-flaky retry count independent of business attempt budget.';
COMMENT ON COLUMN agent_goal_attempt.previous_failure_class IS 'Audit copy of legacy failure_class before responsibility-class migration.';

-- Legacy structural and semantic classes described invalid model output.
-- Legacy transient was the only non-model bucket and was not reliably attributable;
-- map it to flaky as the least destructive retryable responsibility class while
-- preserving the original literal in previous_failure_class for rollback/audit.
UPDATE agent_goal_attempt
SET previous_failure_class = failure_class,
    failure_class = CASE failure_class
        WHEN 'structural' THEN 'model'
        WHEN 'semantic' THEN 'model'
        WHEN 'transient' THEN 'flaky'
        ELSE failure_class
    END
WHERE failure_class IN ('structural', 'semantic', 'transient');

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

ALTER TABLE agent_goal DROP CONSTRAINT IF EXISTS agent_goal_session_id_fkey;
DROP INDEX IF EXISTS uniq_agent_goal_session;
DROP INDEX IF EXISTS idx_agent_goal_dispatchable;
ALTER TABLE agent_goal DROP CONSTRAINT IF EXISTS agent_goal_check3;
ALTER TABLE agent_goal DROP CONSTRAINT IF EXISTS agent_goal_check4;

UPDATE agent_goal
SET lifecycle = CASE
        WHEN lifecycle = 'ready' THEN 'pending'
        WHEN lifecycle = 'accepted' THEN 'done'
        WHEN lifecycle IN ('rejected_final', 'abandoned', 'cancelled') THEN 'done'
        WHEN lifecycle = 'blocked' AND block_reason = 'dep' THEN 'pending'
        ELSE lifecycle
    END,
    done_reason = CASE
        WHEN lifecycle = 'accepted' THEN 'accepted'
        WHEN lifecycle IN ('rejected_final', 'abandoned') THEN 'failed'
        WHEN lifecycle = 'cancelled' THEN 'cancelled'
        ELSE ''
    END,
    block_reason = CASE
        WHEN lifecycle = 'blocked' AND block_reason = 'dep' THEN ''
        ELSE block_reason
    END;

ALTER TABLE agent_goal DROP COLUMN session_id;
ALTER TABLE agent_goal DROP COLUMN required_total;
ALTER TABLE agent_goal DROP COLUMN required_accepted;
ALTER TABLE agent_goal DROP COLUMN required_failed;
ALTER TABLE agent_goal DROP COLUMN required_blocked;

ALTER TABLE agent_goal ADD CONSTRAINT agent_goal_done_reason_check CHECK (
    (lifecycle = 'done' AND done_reason IN ('accepted', 'failed', 'cancelled'))
    OR (lifecycle <> 'done' AND done_reason = '')
);
ALTER TABLE agent_goal ADD CONSTRAINT agent_goal_done_accepted_check CHECK (
    done_reason <> 'accepted'
    OR (acceptance_state = 'passed' AND accepted_output IS NOT NULL)
);
CREATE INDEX idx_agent_goal_dispatchable ON agent_goal (priority DESC, created_at)
WHERE lifecycle = 'pending' AND active_attempt_id IS NULL AND kind = 'leaf';

-- +goose Down
DROP INDEX IF EXISTS idx_agent_goal_dispatchable;
ALTER TABLE agent_goal DROP CONSTRAINT IF EXISTS agent_goal_done_accepted_check;
ALTER TABLE agent_goal DROP CONSTRAINT IF EXISTS agent_goal_done_reason_check;

ALTER TABLE agent_goal ADD COLUMN session_id text NOT NULL DEFAULT '';
ALTER TABLE agent_goal ADD COLUMN required_total bigint NOT NULL DEFAULT 0;
ALTER TABLE agent_goal ADD COLUMN required_accepted bigint NOT NULL DEFAULT 0;
ALTER TABLE agent_goal ADD COLUMN required_failed bigint NOT NULL DEFAULT 0;
ALTER TABLE agent_goal ADD COLUMN required_blocked bigint NOT NULL DEFAULT 0;

UPDATE agent_goal
SET lifecycle = CASE
        WHEN lifecycle = 'pending' THEN 'ready'
        WHEN lifecycle = 'done' AND done_reason = 'accepted' THEN 'accepted'
        WHEN lifecycle = 'done' AND done_reason = 'cancelled' THEN 'cancelled'
        WHEN lifecycle = 'done' THEN 'rejected_final'
        ELSE lifecycle
    END,
    block_reason = CASE WHEN lifecycle = 'pending' THEN '' ELSE block_reason END;

ALTER TABLE agent_goal DROP COLUMN done_reason;
ALTER TABLE agent_goal ADD CONSTRAINT agent_goal_check3 CHECK ((required_total >= 0) AND (required_accepted >= 0) AND (required_failed >= 0) AND (required_blocked >= 0));
ALTER TABLE agent_goal ADD CONSTRAINT agent_goal_check4 CHECK ((lifecycle <> 'accepted') OR ((acceptance_state = 'passed') AND (accepted_output IS NOT NULL)));
CREATE INDEX idx_agent_goal_dispatchable ON agent_goal (priority DESC, created_at)
WHERE lifecycle = 'ready' AND active_attempt_id IS NULL AND kind = 'leaf';
-- Lossy down: the dropped session ids are unrecoverable, so every restored row
-- holds the '' default. The unique index is partial so this Down still applies
-- on a populated table; uniqueness resumes for real (non-empty) values only.
CREATE UNIQUE INDEX uniq_agent_goal_session ON agent_goal (session_id) WHERE session_id <> '';

ALTER TABLE agent_goal DROP COLUMN budget_bonus;

DROP TABLE agent_goal_event;

UPDATE agent_goal_attempt
SET failure_class = CASE
        WHEN previous_failure_class <> '' THEN previous_failure_class
        WHEN failure_class = 'model' THEN 'semantic'
        WHEN failure_class = 'environment' THEN 'transient'
        WHEN failure_class = 'contract' THEN 'semantic'
        WHEN failure_class = 'flaky' THEN 'transient'
        ELSE failure_class
    END;

ALTER TABLE agent_goal_attempt
    DROP COLUMN previous_failure_class;

ALTER TABLE agent_goal
    DROP CONSTRAINT agent_goal_flaky_count_check,
    DROP COLUMN flaky_count;

ALTER TABLE agent_goal_attempt
    DROP CONSTRAINT agent_goal_attempt_repair_rounds_check,
    DROP COLUMN repair_rounds;
