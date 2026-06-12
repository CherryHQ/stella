-- Add column "result_message_id" to table: "ctx_group_dispatch"
ALTER TABLE `ctx_group_dispatch` ADD COLUMN `result_message_id` text NOT NULL DEFAULT '';
