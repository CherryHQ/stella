-- Add column "reasoning" to table: "ctx_group_message"
ALTER TABLE `ctx_group_message` ADD COLUMN `reasoning` text NOT NULL DEFAULT '';
