-- Create index "idx_ctx_conversation_user_agent_active" to table: "ctx_conversation"
CREATE INDEX `idx_ctx_conversation_user_agent_active` ON `ctx_conversation` (`user_id`, `agent_id`, `archived`, `last_active` DESC);
-- Create index "idx_ctx_conversation_user_agent_kind_active" to table: "ctx_conversation"
CREATE INDEX `idx_ctx_conversation_user_agent_kind_active` ON `ctx_conversation` (`user_id`, `agent_id`, `kind`, `archived`, `last_active` DESC);
-- Create index "idx_ctx_conversation_review_agent_active" to table: "ctx_conversation"
CREATE INDEX `idx_ctx_conversation_review_agent_active` ON `ctx_conversation` (`agent_id`, `archived`, `last_active` DESC);
-- Create index "idx_agent_goal_user_created" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_user_created` ON `agent_goal` (`user_id`, `created_at` DESC, `id` DESC);
-- Create index "idx_agent_goal_planning_candidates" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_planning_candidates` ON `agent_goal` (`priority` DESC, `created_at`) WHERE status = 'draft';
-- Create index "idx_agent_goal_synthesis_candidates" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_synthesis_candidates` ON `agent_goal` (`priority` DESC, `updated_at`) WHERE status = 'running' AND review_policy != 'none';
-- Create index "idx_agent_task_user_created" to table: "agent_task"
CREATE INDEX `idx_agent_task_user_created` ON `agent_task` (`user_id`, `created_at` DESC, `id` DESC);
-- Create index "idx_agent_task_user_agent_status_created" to table: "agent_task"
CREATE INDEX `idx_agent_task_user_agent_status_created` ON `agent_task` (`user_id`, `agent_id`, `status`, `created_at` DESC, `id` DESC);
-- Create index "idx_agent_task_user_agent_project_created" to table: "agent_task"
CREATE INDEX `idx_agent_task_user_agent_project_created` ON `agent_task` (`user_id`, `agent_id`, `project_id`, `created_at` DESC, `id` DESC);
-- Create index "idx_agent_task_ready_candidates" to table: "agent_task"
CREATE INDEX `idx_agent_task_ready_candidates` ON `agent_task` (`priority` DESC, `created_at`) WHERE status = 'ready' AND active_run_id IS NULL;
