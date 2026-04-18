-- Create "skills" table
CREATE TABLE `skills` (
  `id` text NULL,
  `scope` text NOT NULL,
  `user_id` integer NULL,
  `agent_id` text NULL,
  `project` text NULL,
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
  CHECK (scope IN ('system','agent','user','project')),
  CHECK (status IN ('draft','active','deprecated')),
  CHECK (
        (scope='system'  AND user_id IS NULL     AND agent_id IS NULL     AND project IS NULL) OR
        (scope='agent'   AND user_id IS NULL     AND agent_id IS NOT NULL AND project IS NULL) OR
        (scope='user'    AND user_id IS NOT NULL AND agent_id IS NULL     AND project IS NULL) OR
        (scope='project' AND user_id IS NULL     AND agent_id IS NULL     AND project IS NOT NULL)
    )
);
-- Create index "idx_skills_owner_name" to table: "skills"
CREATE UNIQUE INDEX `idx_skills_owner_name` ON `skills` (`name`, `scope`, (ifnull(user_id, 0)), (ifnull(agent_id, '')), (ifnull(project, '')));
-- Create index "idx_skills_visibility" to table: "skills"
CREATE INDEX `idx_skills_visibility` ON `skills` (`scope`, `user_id`, `agent_id`, `project`);
-- Create "skill_files" table
CREATE TABLE `skill_files` (
  `skill_id` text NOT NULL,
  `path` text NOT NULL,
  `content` text NOT NULL,
  PRIMARY KEY (`skill_id`, `path`),
  CONSTRAINT `0` FOREIGN KEY (`skill_id`) REFERENCES `skills` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
