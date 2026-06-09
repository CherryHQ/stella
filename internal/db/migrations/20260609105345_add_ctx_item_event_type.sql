-- Add column "event_type" to table: "ctx_item"
ALTER TABLE `ctx_item` ADD COLUMN `event_type` text NOT NULL DEFAULT '';
-- Backfill event_type from ctx_message for existing message items.
UPDATE ctx_item SET event_type = COALESCE(
    (SELECT m.event_type FROM ctx_message m WHERE m.id = ctx_item.message_id),
    ''
) WHERE item_type = 'message' AND message_id IS NOT NULL;
