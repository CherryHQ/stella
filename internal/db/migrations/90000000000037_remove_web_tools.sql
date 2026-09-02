-- +goose Up
-- The builtin `web_search` and `web_fetch` tools are gone: the model reaches
-- the public web through the `web` skill (bun scripts/web.ts search|fetch and
-- Lightpanda site scripts) instead. A tool_override row keyed by a retired
-- name would otherwise wait for a future tool to reuse the name and inherit
-- the setting, so delete it and return the capability to its default.
DELETE FROM tool_override
WHERE tool_name IN ('web_search', 'web_fetch');

-- +goose Down
-- Deliberately a no-op: the deleted rows' scope, owner, and enabled value
-- cannot be reconstructed safely.
SELECT 1;
