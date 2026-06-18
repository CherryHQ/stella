ALTER TABLE `agent_task` ADD COLUMN `archived_at` text;
ALTER TABLE `agent_goal` ADD COLUMN `archived_at` text;

CREATE INDEX `idx_agent_task_user_archived_created`
    ON `agent_task` (`user_id`, `archived_at`, `created_at` DESC, `id` DESC);
CREATE INDEX `idx_agent_goal_user_archived_created`
    ON `agent_goal` (`user_id`, `archived_at`, `created_at` DESC, `id` DESC);
