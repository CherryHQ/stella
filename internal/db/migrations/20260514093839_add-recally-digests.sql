-- Create "recally_digests" table
CREATE TABLE `recally_digests` (
  `id` text NOT NULL,
  `user_id` integer NOT NULL,
  `date` text NOT NULL,
  `narrative` text NOT NULL DEFAULT '',
  `saved_yesterday_count` integer NOT NULL DEFAULT 0,
  `unread_count` integer NOT NULL DEFAULT 0,
  `read_count` integer NOT NULL DEFAULT 0,
  `archived_count` integer NOT NULL DEFAULT 0,
  `starred_count` integer NOT NULL DEFAULT 0,
  `worth_revisiting_count` integer NOT NULL DEFAULT 0,
  `total_articles` integer NOT NULL DEFAULT 0,
  `top_tags` text NOT NULL DEFAULT '[]',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_recally_digests_user_date" to table: "recally_digests"
CREATE UNIQUE INDEX `idx_recally_digests_user_date` ON `recally_digests` (`user_id`, `date`);
-- Create index "idx_recally_digests_user_id" to table: "recally_digests"
CREATE INDEX `idx_recally_digests_user_id` ON `recally_digests` (`user_id`);
-- Create "recally_digest_articles" table
CREATE TABLE `recally_digest_articles` (
  `digest_id` text NOT NULL,
  `article_id` text NOT NULL,
  `section` text NOT NULL,
  `position` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`digest_id`, `article_id`, `section`),
  CONSTRAINT `0` FOREIGN KEY (`article_id`) REFERENCES `articles` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`digest_id`) REFERENCES `recally_digests` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (section IN ('saved_yesterday', 'worth_revisiting'))
);
-- Create index "idx_recally_digest_articles_digest" to table: "recally_digest_articles"
CREATE INDEX `idx_recally_digest_articles_digest` ON `recally_digest_articles` (`digest_id`);
