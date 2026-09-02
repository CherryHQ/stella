-- +goose Up
-- The `tool/tap-web` plugin (Tap, agent-browser, Lightpanda) no longer exists
-- in code: web access is now served by the builtin `web_search` and
-- `web_fetch` tools. Rows keyed by the retired plugin ID would otherwise linger
-- forever, since nothing resolves that ID anymore.
DELETE FROM plugin_override
WHERE plugin_id = 'tool/tap-web';

DELETE FROM plugin_state
WHERE plugin_id = 'tool/tap-web';

DELETE FROM plugin
WHERE id = 'tool/tap-web';

-- +goose Down
-- Deliberately a no-op: the deleted rows' config, enable state, and secret
-- bindings cannot be reconstructed, and the plugin they described is gone.
SELECT 1;
