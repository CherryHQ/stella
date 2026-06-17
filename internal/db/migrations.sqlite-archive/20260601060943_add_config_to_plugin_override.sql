-- Add column "config" to table: "plugin_override"
ALTER TABLE `plugin_override` ADD COLUMN `config` text NOT NULL DEFAULT '';
