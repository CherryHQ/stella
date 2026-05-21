-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_skills" table
CREATE TABLE `new_skills` (
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
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (scope IN ('system','agent','user')),
  CHECK (status IN ('draft','active','deprecated')),
  CHECK (
        (scope='system'  AND user_id IS NULL     AND agent_id IS NULL) OR
        (scope='agent'   AND user_id IS NULL     AND agent_id IS NOT NULL) OR
        (scope='user'    AND user_id IS NOT NULL)
    )
);
-- Copy rows from old table "skills" to new temporary table "new_skills"
INSERT INTO `new_skills` (`id`, `scope`, `user_id`, `agent_id`, `name`, `description`, `status`, `disable_model_invocation`, `metadata`, `created_at`, `updated_at`) SELECT `id`, `scope`, `user_id`, `agent_id`, `name`, `description`, `status`, `disable_model_invocation`, `metadata`, `created_at`, `updated_at` FROM `skills`;
-- Drop "skills" table after copying rows
DROP TABLE `skills`;
-- Rename temporary table "new_skills" to "skills"
ALTER TABLE `new_skills` RENAME TO `skills`;
-- Create index "idx_skills_owner_name" to table: "skills"
CREATE UNIQUE INDEX `idx_skills_owner_name` ON `skills` (`name`, `scope`, (ifnull(user_id, 0)), (ifnull(agent_id, '')));
-- Create index "idx_skills_visibility" to table: "skills"
CREATE INDEX `idx_skills_visibility` ON `skills` (`scope`, `user_id`, `agent_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
