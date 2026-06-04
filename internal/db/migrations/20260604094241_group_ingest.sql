-- Create "ctx_group_ingest_cursor" table
CREATE TABLE `ctx_group_ingest_cursor` (
  `group_id` text NOT NULL,
  `pipeline` text NOT NULL DEFAULT 'memory_ingest',
  `last_seq` integer NOT NULL DEFAULT 0,
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`group_id`, `pipeline`),
  CONSTRAINT `0` FOREIGN KEY (`group_id`) REFERENCES `ctx_group_state` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "ctx_group_ingest_error" table
CREATE TABLE `ctx_group_ingest_error` (
  `id` text NOT NULL,
  `group_id` text NOT NULL,
  `pipeline` text NOT NULL,
  `seq` integer NOT NULL,
  `reason` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`group_id`) REFERENCES `ctx_group_state` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_group_ingest_error_dedup" to table: "ctx_group_ingest_error"
CREATE UNIQUE INDEX `idx_group_ingest_error_dedup` ON `ctx_group_ingest_error` (`group_id`, `pipeline`, `seq`);
