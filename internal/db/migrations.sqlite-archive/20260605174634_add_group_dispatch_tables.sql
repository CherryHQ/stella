-- Create "ctx_group_outbox" table
CREATE TABLE `ctx_group_outbox` (
  `id` text NOT NULL,
  `group_message_id` text NOT NULL,
  `group_id` text NOT NULL,
  `envelope` text NOT NULL DEFAULT '{}',
  `status` text NOT NULL DEFAULT 'pending',
  `attempt_count` integer NOT NULL DEFAULT 0,
  `lease_until` text NULL,
  `next_attempt_at` text NULL,
  `last_error` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`group_id`) REFERENCES `ctx_group_state` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`group_message_id`) REFERENCES `ctx_group_message` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "ctx_group_outbox_group_message_id" to table: "ctx_group_outbox"
CREATE UNIQUE INDEX `ctx_group_outbox_group_message_id` ON `ctx_group_outbox` (`group_message_id`);
-- Create index "idx_ctx_group_outbox_group_id" to table: "ctx_group_outbox"
CREATE INDEX `idx_ctx_group_outbox_group_id` ON `ctx_group_outbox` (`group_id`);
-- Create index "idx_ctx_group_outbox_pending" to table: "ctx_group_outbox"
CREATE INDEX `idx_ctx_group_outbox_pending` ON `ctx_group_outbox` (`status`, `next_attempt_at`, `created_at`) WHERE status = 'pending';
-- Create index "idx_ctx_group_outbox_running_lease" to table: "ctx_group_outbox"
CREATE INDEX `idx_ctx_group_outbox_running_lease` ON `ctx_group_outbox` (`status`, `lease_until`) WHERE status = 'running';
-- Create "ctx_group_dispatch" table
CREATE TABLE `ctx_group_dispatch` (
  `id` text NOT NULL,
  `group_message_id` text NOT NULL,
  `group_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `reply_channel_id` text NOT NULL,
  `status` text NOT NULL DEFAULT 'pending',
  `attempt_count` integer NOT NULL DEFAULT 0,
  `lease_until` text NULL,
  `next_attempt_at` text NULL,
  `last_error` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`reply_channel_id`) REFERENCES `channel` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `2` FOREIGN KEY (`group_id`) REFERENCES `ctx_group_state` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `3` FOREIGN KEY (`group_message_id`) REFERENCES `ctx_group_message` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "ctx_group_dispatch_group_message_id_agent_id" to table: "ctx_group_dispatch"
CREATE UNIQUE INDEX `ctx_group_dispatch_group_message_id_agent_id` ON `ctx_group_dispatch` (`group_message_id`, `agent_id`);
-- Create index "idx_ctx_group_dispatch_group_id" to table: "ctx_group_dispatch"
CREATE INDEX `idx_ctx_group_dispatch_group_id` ON `ctx_group_dispatch` (`group_id`);
-- Create index "idx_ctx_group_dispatch_reply_channel" to table: "ctx_group_dispatch"
CREATE INDEX `idx_ctx_group_dispatch_reply_channel` ON `ctx_group_dispatch` (`reply_channel_id`);
-- Create index "idx_ctx_group_dispatch_pending" to table: "ctx_group_dispatch"
CREATE INDEX `idx_ctx_group_dispatch_pending` ON `ctx_group_dispatch` (`status`, `next_attempt_at`, `created_at`) WHERE status = 'pending';
-- Create index "idx_ctx_group_dispatch_running_lease" to table: "ctx_group_dispatch"
CREATE INDEX `idx_ctx_group_dispatch_running_lease` ON `ctx_group_dispatch` (`status`, `lease_until`) WHERE status = 'running';
