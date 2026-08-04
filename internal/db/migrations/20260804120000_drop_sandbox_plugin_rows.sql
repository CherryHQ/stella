-- +goose Up
-- The sandbox backend is now a deploy-time decision read from
-- STELLA_SANDBOX_BACKEND, not a togglable plugin. Its rows in plugin are
-- override rows for builtins that no longer exist, so without this delete they
-- would resurface in the admin plugin list as unknown custom plugins.
DELETE FROM plugin WHERE id LIKE 'sandbox/%';
DELETE FROM plugin_state WHERE plugin_id LIKE 'sandbox/%';

-- +goose Down
-- Irreversible by design: the deleted rows were enable/disable overrides for
-- plugins the code no longer defines, and the backend choice now lives in the
-- environment. Recreating them would restore dead state.
SELECT 1;
