-- Rename "recally_rss_feed" table to "recally_feed" (data-preserving).
ALTER TABLE `recally_rss_feed` RENAME TO `recally_feed`;
-- Rename "recally_rss_feed_entry" table to "recally_feed_entry" (data-preserving).
-- SQLite auto-updates the entry -> feed foreign key to the new table name.
ALTER TABLE `recally_rss_feed_entry` RENAME TO `recally_feed_entry`;
-- Rename indexes to match the new table names (indexes are rebuildable, no data loss).
DROP INDEX `idx_recally_rss_feed_user_url`;
CREATE UNIQUE INDEX `idx_recally_feed_user_url` ON `recally_feed` (`user_id`, `url`);
DROP INDEX `idx_recally_rss_feed_entry_feed_guid`;
CREATE UNIQUE INDEX `idx_recally_feed_entry_feed_guid` ON `recally_feed_entry` (`feed_id`, `guid`);
DROP INDEX `idx_recally_rss_feed_entry_status`;
CREATE INDEX `idx_recally_feed_entry_status` ON `recally_feed_entry` (`status`);
