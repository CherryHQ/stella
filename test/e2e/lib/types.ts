export type PluginScope = "system" | "system_agent" | "user" | "user_agent";

export interface PluginDefinition {
  id: string;
  display_name: string;
  is_builtin: boolean;
  is_default_enabled: boolean;
  spec: Record<string, unknown>;
  revision: number;
  retired_at?: string | null;
  created_at: string;
  updated_at: string;
}

export interface PluginMCPServerSummary {
  transport: "streamable_http" | "sse";
  auth_type: "none" | "bearer" | "oauth";
  credential_mode: "shared" | "per_user";
  endpoint_configured: boolean;
  bearer_configured: boolean;
  oauth_client_id_configured: boolean;
  oauth_client_secret_configured: boolean;
}

export interface PluginConfig {
  id: string;
  plugin_id: string;
  scope: PluginScope;
  user_id?: string;
  agent_id?: string;
  is_enabled: boolean | null;
  resource_summary: {
    mcp_servers: PluginMCPServerSummary[];
    binaries?: unknown[];
    skills?: unknown[];
    session_env?: unknown[];
    oauth_provider_configured?: boolean;
  };
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface McpServer {
  id: string;
  plugin_id: string;
  parent_config_id: string;
  parent_revision: number;
  server_key: string;
  scope: PluginScope;
  enabled: boolean;
  credential_mode: "shared" | "per_user";
  auth_type?: "none" | "bearer" | "oauth";
  transport?: "streamable_http" | "sse";
  endpoint_configured?: boolean;
  bearer_configured?: boolean;
  oauth_client_id_configured?: boolean;
  oauth_client_secret_configured?: boolean;
  needs_auth: boolean;
  status: string;
  status_error?: string;
  tools: Array<{ name: string; }>;
  revision: number;
}

export interface CreatePluginResponse {
  plugin: PluginDefinition;
  config: PluginConfig;
}
export interface AgentTool {
  name: string;
  enabled: boolean;
  scope: string;
  description?: string;
  [key: string]: unknown;
}
export interface RegistryServer {
  source: string;
  id: string;
  name: string;
  url: string;
  transport: string;
  auth: string;
  version?: string;
  headers?: { name: string; template?: string; }[];
  [key: string]: unknown;
}
