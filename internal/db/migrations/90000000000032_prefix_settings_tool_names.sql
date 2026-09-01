-- +goose Up
-- Settings tools are being renamed into their own model-facing namespace. A
-- tool_override row is keyed by name, so rows for the retired names would
-- otherwise become inert and could be inherited by a future tool that reuses
-- one of those names.
--
-- Stella is pre-production, so this is a clean break rather than an
-- expand-then-contract migration. Deleting the rows restores the default
-- visibility decision; preserving them would require knowing whether each
-- old row was an intentional deny or allow for its replacement.
DELETE FROM tool_override
WHERE tool_name IN (
    'agent_list',
    'agent_get',
    'agent_create',
    'agent_update',
    'agent_delete',
    'agent_tool_list',
    'agent_tool_update',
    'agent_tool_delete',
    'library_file_list',
    'library_file_get',
    'library_file_upload',
    'library_file_delete',
    'skill_list',
    'skill_get',
    'skill_create',
    'skill_update',
    'skill_delete',
    'provider_list',
    'provider_get',
    'provider_create',
    'provider_update',
    'provider_delete',
    'default_model_get',
    'default_model_update',
    'embedding_setting_get',
    'embedding_setting_update',
    'plugin_list',
    'plugin_enable',
    'plugin_disable',
    'mcp_server_list',
    'mcp_server_get',
    'mcp_server_create',
    'mcp_server_update',
    'mcp_server_delete'
);

-- "stella" is the reserved durable ID of the built-in Agent. Enable its
-- conversational Settings surface for existing deployments; ordinary Agents
-- retain their default-off policy.
UPDATE agent
SET system_settings_tools_enabled = true,
    updated_at = now()
WHERE id = 'stella';

-- +goose Down
-- Deliberately a no-op: neither the deleted override rows nor the built-in
-- Stella Agent's prior Settings policy can be reconstructed safely.
SELECT 1;
