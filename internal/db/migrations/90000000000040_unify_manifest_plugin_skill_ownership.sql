-- +goose Up
-- Manifest plugins own their enabled state. Copy the legacy generic rows into
-- the manifest override table before the host stops reading those rows for
-- these IDs. A disabled value wins conflicts, while config and vault columns
-- already present in plugin_override remain untouched.
INSERT INTO plugin_override (plugin_id, enabled)
SELECT id, enabled
FROM plugin
WHERE id IN ('tool/gh', 'tool/lark-cli', 'tool/mise')
ON CONFLICT (plugin_id) DO UPDATE
SET enabled = CASE
    WHEN plugin_override.enabled IS FALSE OR EXCLUDED.enabled IS FALSE THEN FALSE
    ELSE EXCLUDED.enabled
END,
updated_at = now();

-- +goose Down
-- The legacy plugin rows are intentionally retained as dormant compatibility
-- data, and existing manifest overrides cannot be reconstructed safely.
SELECT 1;
