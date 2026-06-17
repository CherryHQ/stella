-- Create index "idx_ctx_summary_parent_parent" to table: "ctx_summary_parent"
CREATE INDEX `idx_ctx_summary_parent_parent` ON `ctx_summary_parent` (`parent_summary_id`, `ordinal`);
-- Add column "role" to table: "ctx_item"
ALTER TABLE `ctx_item` ADD COLUMN `role` text NOT NULL DEFAULT '';
-- Backfill role from ctx_message for existing message items.
UPDATE ctx_item SET role = COALESCE(
    (SELECT m.role FROM ctx_message m WHERE m.id = ctx_item.message_id),
    ''
) WHERE message_id IS NOT NULL;
