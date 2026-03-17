-- Create "settings" table
CREATE TABLE `settings` (
  `key` text NULL,
  `value` text NOT NULL DEFAULT '{}',
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`key`)
);
-- Create "settings_providers" table
CREATE TABLE `settings_providers` (
  `id` text NULL,
  `name` text NOT NULL,
  `api_key` text NOT NULL DEFAULT '',
  `base_url` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create "settings_agents" table
CREATE TABLE `settings_agents` (
  `id` text NULL,
  `name` text NOT NULL,
  `provider_id` text NOT NULL,
  `model` text NOT NULL DEFAULT '',
  `model_strong` text NOT NULL DEFAULT '',
  `model_fast` text NOT NULL DEFAULT '',
  `system_prompt` text NOT NULL DEFAULT '',
  `workspace` text NOT NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`provider_id`) REFERENCES `settings_providers` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "settings_channels" table
CREATE TABLE `settings_channels` (
  `id` text NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create "settings_users" table
CREATE TABLE `settings_users` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `external_id` text NOT NULL,
  `platform` text NOT NULL,
  `name` text NOT NULL DEFAULT '',
  `default_agent_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  CONSTRAINT `0` FOREIGN KEY (`default_agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "settings_users_external_id_platform" to table: "settings_users"
CREATE UNIQUE INDEX `settings_users_external_id_platform` ON `settings_users` (`external_id`, `platform`);
-- Create "settings_channel_agents" table
CREATE TABLE `settings_channel_agents` (
  `platform` text NOT NULL,
  `chat_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`platform`, `chat_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "ctx_agent_memory" table
CREATE TABLE `ctx_agent_memory` (
  `user_id` integer NOT NULL,
  `agent_id` text NOT NULL,
  `content` text NOT NULL DEFAULT '',
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`user_id`, `agent_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `settings_users` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "ctx_conversations" table
CREATE TABLE `ctx_conversations` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `session_id` text NOT NULL,
  `title` text NULL,
  `channel` text NOT NULL DEFAULT '',
  `archived` integer NOT NULL DEFAULT 0,
  `last_active` text NOT NULL DEFAULT (datetime('now')),
  `bootstrapped_at` text NULL,
  `agent_id` text NULL,
  `user_id` integer NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now'))
);
-- Create index "ctx_conversations_session_id" to table: "ctx_conversations"
CREATE UNIQUE INDEX `ctx_conversations_session_id` ON `ctx_conversations` (`session_id`);
-- Create "ctx_messages" table
CREATE TABLE `ctx_messages` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `conversation_id` integer NOT NULL,
  `seq` integer NOT NULL,
  `role` text NOT NULL,
  `event_type` text NOT NULL DEFAULT 'text',
  `content` text NOT NULL,
  `token_count` integer NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  CONSTRAINT `0` FOREIGN KEY (`conversation_id`) REFERENCES `ctx_conversations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (role IN ('user', 'assistant', 'tool'))
);
-- Create index "ctx_messages_conversation_id_seq" to table: "ctx_messages"
CREATE UNIQUE INDEX `ctx_messages_conversation_id_seq` ON `ctx_messages` (`conversation_id`, `seq`);
-- Create index "idx_ctx_messages_conv_seq" to table: "ctx_messages"
CREATE INDEX `idx_ctx_messages_conv_seq` ON `ctx_messages` (`conversation_id`, `seq`);
-- Create "ctx_message_parts" table
CREATE TABLE `ctx_message_parts` (
  `id` text NULL,
  `message_id` integer NOT NULL,
  `part_type` text NOT NULL,
  `ordinal` integer NOT NULL,
  `text_content` text NULL,
  `tool_call_id` text NULL,
  `tool_name` text NULL,
  `tool_input` text NULL,
  `tool_output` text NULL,
  `metadata` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`message_id`) REFERENCES `ctx_messages` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (part_type IN ('text', 'reasoning', 'tool'))
);
-- Create index "ctx_message_parts_message_id_ordinal" to table: "ctx_message_parts"
CREATE UNIQUE INDEX `ctx_message_parts_message_id_ordinal` ON `ctx_message_parts` (`message_id`, `ordinal`);
-- Create "ctx_summaries" table
CREATE TABLE `ctx_summaries` (
  `id` text NULL,
  `conversation_id` integer NOT NULL,
  `kind` text NOT NULL,
  `depth` integer NOT NULL DEFAULT 0,
  `content` text NOT NULL,
  `token_count` integer NOT NULL,
  `earliest_at` text NULL,
  `latest_at` text NULL,
  `descendant_count` integer NOT NULL DEFAULT 0,
  `descendant_token_count` integer NOT NULL DEFAULT 0,
  `source_message_token_count` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`conversation_id`) REFERENCES `ctx_conversations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (kind IN ('leaf', 'condensed'))
);
-- Create index "idx_ctx_summaries_conv" to table: "ctx_summaries"
CREATE INDEX `idx_ctx_summaries_conv` ON `ctx_summaries` (`conversation_id`, `created_at`);
-- Create "ctx_summary_messages" table
CREATE TABLE `ctx_summary_messages` (
  `summary_id` text NOT NULL,
  `message_id` integer NOT NULL,
  `ordinal` integer NOT NULL,
  PRIMARY KEY (`summary_id`, `message_id`),
  CONSTRAINT `0` FOREIGN KEY (`message_id`) REFERENCES `ctx_messages` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`summary_id`) REFERENCES `ctx_summaries` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "ctx_summary_parents" table
CREATE TABLE `ctx_summary_parents` (
  `summary_id` text NOT NULL,
  `parent_summary_id` text NOT NULL,
  `ordinal` integer NOT NULL,
  PRIMARY KEY (`summary_id`, `parent_summary_id`),
  CONSTRAINT `0` FOREIGN KEY (`parent_summary_id`) REFERENCES `ctx_summaries` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`summary_id`) REFERENCES `ctx_summaries` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "ctx_items" table
CREATE TABLE `ctx_items` (
  `conversation_id` integer NOT NULL,
  `ordinal` integer NOT NULL,
  `item_type` text NOT NULL,
  `message_id` integer NULL,
  `summary_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`conversation_id`, `ordinal`),
  CONSTRAINT `0` FOREIGN KEY (`summary_id`) REFERENCES `ctx_summaries` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`message_id`) REFERENCES `ctx_messages` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `2` FOREIGN KEY (`conversation_id`) REFERENCES `ctx_conversations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (item_type IN ('message', 'summary')),
  CHECK (
        (item_type = 'message' AND message_id IS NOT NULL AND summary_id IS NULL) OR
        (item_type = 'summary' AND summary_id IS NOT NULL AND message_id IS NULL)
    )
);
-- Create index "idx_ctx_items_conv" to table: "ctx_items"
CREATE INDEX `idx_ctx_items_conv` ON `ctx_items` (`conversation_id`, `ordinal`);
-- Create "sched_jobs" table
CREATE TABLE `sched_jobs` (
  `id` text NULL,
  `name` text NOT NULL,
  `schedule_cron` text NOT NULL DEFAULT '',
  `schedule_every` text NOT NULL DEFAULT '',
  `schedule_at` text NOT NULL DEFAULT '',
  `message` text NOT NULL,
  `session_mode` text NOT NULL DEFAULT 'reuse',
  `enabled` integer NOT NULL DEFAULT 1,
  `agent_id` text NULL,
  `user_id` integer NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
