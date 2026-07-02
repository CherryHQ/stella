-- +goose Up
ALTER TABLE agent_goal
    ADD COLUMN flaky_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN blocked_by text NOT NULL DEFAULT '',
    ADD CONSTRAINT agent_goal_flaky_count_check CHECK (flaky_count >= 0);

ALTER TABLE agent_goal_attempt
    ADD COLUMN previous_failure_class text NOT NULL DEFAULT '';

COMMENT ON COLUMN agent_goal.blocked_by IS 'Responsibility-specific blocked cause for environment or contract failures.';
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

-- +goose Down
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
    DROP COLUMN blocked_by,
    DROP COLUMN flaky_count;
