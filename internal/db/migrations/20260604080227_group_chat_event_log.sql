-- Create "ctx_group_state" table
CREATE TABLE `ctx_group_state` (
  `id` text NULL,
  `platform` text NOT NULL,
  `platform_group_id` text NOT NULL,
  `platform_thread_id` text NOT NULL DEFAULT '',
  `next_seq` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create index "ctx_group_state_platform_platform_group_id_platform_thread_id" to table: "ctx_group_state"
CREATE UNIQUE INDEX `ctx_group_state_platform_platform_group_id_platform_thread_id` ON `ctx_group_state` (`platform`, `platform_group_id`, `platform_thread_id`);
-- Create "ctx_group_message" table
CREATE TABLE `ctx_group_message` (
  `id` text NULL,
  `group_id` text NOT NULL,
  `seq` integer NOT NULL,
  `source_channel_id` text NULL,
  `actor_type` text NOT NULL,
  `actor_id` text NOT NULL,
  `platform_message_id` text NULL,
  `reply_to` text NULL,
  `platform_timestamp` text NULL,
  `idempotency_key` text NULL,
  `content` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`group_id`) REFERENCES `ctx_group_state` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "ctx_group_message_group_id_seq" to table: "ctx_group_message"
CREATE UNIQUE INDEX `ctx_group_message_group_id_seq` ON `ctx_group_message` (`group_id`, `seq`);
-- Create index "idx_ctx_group_message_platform_msg" to table: "ctx_group_message"
CREATE UNIQUE INDEX `idx_ctx_group_message_platform_msg` ON `ctx_group_message` (`group_id`, `platform_message_id`) WHERE platform_message_id IS NOT NULL AND platform_message_id != '';
-- Create index "idx_ctx_group_message_idem" to table: "ctx_group_message"
CREATE UNIQUE INDEX `idx_ctx_group_message_idem` ON `ctx_group_message` (`idempotency_key`) WHERE idempotency_key IS NOT NULL;
