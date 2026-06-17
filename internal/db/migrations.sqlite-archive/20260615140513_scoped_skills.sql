-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_skill" table
CREATE TABLE `new_skill` (
  `id` text NULL,
  `scope` text NOT NULL,
  `user_id` text NULL,
  `agent_id` text NULL,
  `name` text NOT NULL,
  `description` text NOT NULL,
  `status` text NOT NULL DEFAULT 'active',
  `disable_model_invocation` integer NOT NULL DEFAULT 0,
  `metadata` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (
        (scope='user'         AND user_id IS NOT NULL AND agent_id IS NULL) OR
        (scope='user_agent'   AND user_id IS NOT NULL AND agent_id IS NOT NULL) OR
        (scope='system'       AND user_id IS NULL     AND agent_id IS NULL) OR
        (scope='system_agent' AND user_id IS NULL     AND agent_id IS NOT NULL)
    )
);
-- Copy rows from old table "skill" to new temporary table "new_skill".
-- Migrate the old three-scope vocabulary (system|agent|user) into the four-scope
-- model: admin-managed agent skills become system_agent, and personal skills
-- pinned to an agent become user_agent. system and bare-user rows are unchanged.
INSERT INTO `new_skill` (`id`, `scope`, `user_id`, `agent_id`, `name`, `description`, `status`, `disable_model_invocation`, `metadata`, `created_at`, `updated_at`) SELECT `id`,
  CASE
    WHEN `scope` = 'agent' THEN 'system_agent'
    WHEN `scope` = 'user' AND `agent_id` IS NOT NULL THEN 'user_agent'
    ELSE `scope`
  END,
  `user_id`, `agent_id`, `name`, `description`, `status`, `disable_model_invocation`, `metadata`, `created_at`, `updated_at` FROM `skill`;
-- Drop "skill" table after copying rows
DROP TABLE `skill`;
-- Rename temporary table "new_skill" to "skill"
ALTER TABLE `new_skill` RENAME TO `skill`;
-- Create index "idx_skill_owner_name" to table: "skill"
CREATE UNIQUE INDEX `idx_skill_owner_name` ON `skill` (`name`, `scope`, (ifnull(user_id, 0)), (ifnull(agent_id, '')));
-- Create index "idx_skill_visibility" to table: "skill"
CREATE INDEX `idx_skill_visibility` ON `skill` (`scope`, `user_id`, `agent_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
