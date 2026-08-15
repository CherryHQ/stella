-- +goose Up
-- RTK no longer rewrites bash tool calls. Remove both the code-plugin row and
-- any manifest override so the deleted integration cannot remain visible or
-- retain runtime state after upgrade.
DELETE FROM plugin WHERE id = 'hook/rtk';
DELETE FROM plugin_state WHERE plugin_id = 'hook/rtk';
DELETE FROM plugin_override WHERE plugin_id = 'hook/rtk';

-- +goose Down
-- Irreversible by design: RTK is no longer shipped or registered, so restoring
-- its stale configuration would create a dead plugin entry.
SELECT 1;
