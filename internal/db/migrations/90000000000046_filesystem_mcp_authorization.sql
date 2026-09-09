-- +goose Up
SET LOCAL lock_timeout = '5s';

-- File declarations have stable authentication identities but no catalog row.
-- Keep existing flows and their captured targets while allowing those identities.
ALTER TABLE public.mcp_oauth_flow
    DROP CONSTRAINT mcp_oauth_flow_server_id_plugin_config_fkey;

-- +goose Down
-- Reinstating the catalog foreign key would discard active file-backed flows.
SELECT 1;
