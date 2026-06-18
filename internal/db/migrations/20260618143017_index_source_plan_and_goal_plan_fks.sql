-- Create index "idx_agent_task_source_plan_created" to table: "agent_task"
CREATE INDEX `idx_agent_task_source_plan_created` ON `agent_task` (`source_plan_id`, `created_at`, `id`) WHERE source_plan_id IS NOT NULL;
-- Create index "idx_agent_goal_plan_source_run" to table: "agent_goal_plan"
CREATE INDEX `idx_agent_goal_plan_source_run` ON `agent_goal_plan` (`source_run_id`);
-- Create index "idx_agent_goal_plan_approved_review" to table: "agent_goal_plan"
CREATE INDEX `idx_agent_goal_plan_approved_review` ON `agent_goal_plan` (`approved_review_id`);
