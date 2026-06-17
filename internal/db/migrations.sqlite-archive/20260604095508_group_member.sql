-- Create "channel_group_member" table
CREATE TABLE `channel_group_member` (
  `group_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `reply_channel_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`group_id`, `agent_id`),
  CONSTRAINT `0` FOREIGN KEY (`reply_channel_id`) REFERENCES `channel` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `2` FOREIGN KEY (`group_id`) REFERENCES `ctx_group_state` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
