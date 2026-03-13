-- Create "conversations" table
CREATE TABLE `conversations` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `session_id` text NOT NULL,
  `title` text NULL,
  `channel` text NOT NULL DEFAULT '',
  `archived` integer NOT NULL DEFAULT 0,
  `last_active` text NOT NULL DEFAULT (datetime('now')),
  `bootstrapped_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now'))
);
-- Create index "conversations_session_id" to table: "conversations"
CREATE UNIQUE INDEX `conversations_session_id` ON `conversations` (`session_id`);
-- Create "messages" table
CREATE TABLE `messages` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `conversation_id` integer NOT NULL,
  `seq` integer NOT NULL,
  `role` text NOT NULL,
  `event_type` text NOT NULL DEFAULT 'text',
  `content` text NOT NULL,
  `token_count` integer NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  CONSTRAINT `0` FOREIGN KEY (`conversation_id`) REFERENCES `conversations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (role IN ('user', 'assistant', 'tool'))
);
-- Create index "messages_conversation_id_seq" to table: "messages"
CREATE UNIQUE INDEX `messages_conversation_id_seq` ON `messages` (`conversation_id`, `seq`);
-- Create index "idx_messages_conv_seq" to table: "messages"
CREATE INDEX `idx_messages_conv_seq` ON `messages` (`conversation_id`, `seq`);
-- Create "message_parts" table
CREATE TABLE `message_parts` (
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
  CONSTRAINT `0` FOREIGN KEY (`message_id`) REFERENCES `messages` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (part_type IN ('text', 'reasoning', 'tool'))
);
-- Create index "message_parts_message_id_ordinal" to table: "message_parts"
CREATE UNIQUE INDEX `message_parts_message_id_ordinal` ON `message_parts` (`message_id`, `ordinal`);
-- Create "summaries" table
CREATE TABLE `summaries` (
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
  CONSTRAINT `0` FOREIGN KEY (`conversation_id`) REFERENCES `conversations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (kind IN ('leaf', 'condensed'))
);
-- Create index "idx_summaries_conv" to table: "summaries"
CREATE INDEX `idx_summaries_conv` ON `summaries` (`conversation_id`, `created_at`);
-- Create "summary_messages" table
CREATE TABLE `summary_messages` (
  `summary_id` text NOT NULL,
  `message_id` integer NOT NULL,
  `ordinal` integer NOT NULL,
  PRIMARY KEY (`summary_id`, `message_id`),
  CONSTRAINT `0` FOREIGN KEY (`message_id`) REFERENCES `messages` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`summary_id`) REFERENCES `summaries` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "summary_parents" table
CREATE TABLE `summary_parents` (
  `summary_id` text NOT NULL,
  `parent_summary_id` text NOT NULL,
  `ordinal` integer NOT NULL,
  PRIMARY KEY (`summary_id`, `parent_summary_id`),
  CONSTRAINT `0` FOREIGN KEY (`parent_summary_id`) REFERENCES `summaries` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`summary_id`) REFERENCES `summaries` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "context_items" table
CREATE TABLE `context_items` (
  `conversation_id` integer NOT NULL,
  `ordinal` integer NOT NULL,
  `item_type` text NOT NULL,
  `message_id` integer NULL,
  `summary_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`conversation_id`, `ordinal`),
  CONSTRAINT `0` FOREIGN KEY (`summary_id`) REFERENCES `summaries` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`message_id`) REFERENCES `messages` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `2` FOREIGN KEY (`conversation_id`) REFERENCES `conversations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (item_type IN ('message', 'summary')),
  CHECK (
        (item_type = 'message' AND message_id IS NOT NULL AND summary_id IS NULL) OR
        (item_type = 'summary' AND summary_id IS NOT NULL AND message_id IS NULL)
    )
);
-- Create index "idx_context_items_conv" to table: "context_items"
CREATE INDEX `idx_context_items_conv` ON `context_items` (`conversation_id`, `ordinal`);
