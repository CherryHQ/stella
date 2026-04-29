-- Create "articles" table
CREATE TABLE `articles` (
  `id` text NULL,
  `user_id` integer NOT NULL,
  `agent_id` text NULL,
  `url` text NOT NULL,
  `canonical_url` text NOT NULL,
  `source_type` text NOT NULL DEFAULT 'web',
  `title` text NOT NULL DEFAULT '',
  `author` text NOT NULL DEFAULT '',
  `summary` text NOT NULL DEFAULT '',
  `tags` text NOT NULL DEFAULT '[]',
  `status` text NOT NULL DEFAULT 'unread',
  `starred` integer NOT NULL DEFAULT 0,
  `file_path` text NOT NULL DEFAULT '',
  `metadata` text NOT NULL DEFAULT '{}',
  `published_at` text NULL,
  `saved_at` text NOT NULL DEFAULT (datetime('now')),
  `read_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (source_type IN ('web','twitter','youtube','github','rss','pdf')),
  CHECK (status IN ('unread','read','archived'))
);
-- Create index "idx_articles_user_canonical" to table: "articles"
CREATE UNIQUE INDEX `idx_articles_user_canonical` ON `articles` (`user_id`, `canonical_url`);
-- Create index "idx_articles_user_status" to table: "articles"
CREATE INDEX `idx_articles_user_status` ON `articles` (`user_id`, `status`);
-- Create index "idx_articles_user_source" to table: "articles"
CREATE INDEX `idx_articles_user_source` ON `articles` (`user_id`, `source_type`);
-- Create index "idx_articles_user_starred" to table: "articles"
CREATE INDEX `idx_articles_user_starred` ON `articles` (`user_id`, `starred`) WHERE starred = 1;
-- Create index "idx_articles_saved_at" to table: "articles"
CREATE INDEX `idx_articles_saved_at` ON `articles` (`saved_at`);
-- Create "rss_feeds" table
CREATE TABLE `rss_feeds` (
  `id` text NULL,
  `user_id` integer NOT NULL,
  `agent_id` text NULL,
  `url` text NOT NULL,
  `title` text NOT NULL DEFAULT '',
  `description` text NOT NULL DEFAULT '',
  `check_interval` text NOT NULL DEFAULT '1h',
  `last_checked_at` text NULL,
  `last_etag` text NOT NULL DEFAULT '',
  `last_modified` text NOT NULL DEFAULT '',
  `enabled` integer NOT NULL DEFAULT 1,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_rss_feeds_user_url" to table: "rss_feeds"
CREATE UNIQUE INDEX `idx_rss_feeds_user_url` ON `rss_feeds` (`user_id`, `url`);
-- Create "rss_feed_entries" table
CREATE TABLE `rss_feed_entries` (
  `id` text NULL,
  `feed_id` text NOT NULL,
  `guid` text NOT NULL,
  `url` text NOT NULL DEFAULT '',
  `title` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'pending',
  `article_id` text NULL,
  `attempts` integer NOT NULL DEFAULT 0,
  `error_msg` text NOT NULL DEFAULT '',
  `discovered_at` text NOT NULL DEFAULT (datetime('now')),
  `processed_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`article_id`) REFERENCES `articles` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`feed_id`) REFERENCES `rss_feeds` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (status IN ('pending','saved','skipped','error'))
);
-- Create index "idx_rss_entries_feed_guid" to table: "rss_feed_entries"
CREATE UNIQUE INDEX `idx_rss_entries_feed_guid` ON `rss_feed_entries` (`feed_id`, `guid`);
-- Create index "idx_rss_entries_status" to table: "rss_feed_entries"
CREATE INDEX `idx_rss_entries_status` ON `rss_feed_entries` (`status`);
