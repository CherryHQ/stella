-- Add column "model_thinking" to table: "agent"
ALTER TABLE `agent` ADD COLUMN `model_thinking` text NOT NULL DEFAULT '';
-- Add column "model_strong_thinking" to table: "agent"
ALTER TABLE `agent` ADD COLUMN `model_strong_thinking` text NOT NULL DEFAULT '';
-- Add column "model_fast_thinking" to table: "agent"
ALTER TABLE `agent` ADD COLUMN `model_fast_thinking` text NOT NULL DEFAULT '';
