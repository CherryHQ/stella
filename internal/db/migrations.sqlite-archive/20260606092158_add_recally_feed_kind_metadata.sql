-- Add column "kind" to table: "recally_rss_feed"
ALTER TABLE `recally_rss_feed` ADD COLUMN `kind` text NOT NULL DEFAULT 'rss';
-- Add column "metadata" to table: "recally_rss_feed"
ALTER TABLE `recally_rss_feed` ADD COLUMN `metadata` text NOT NULL DEFAULT '{}';
