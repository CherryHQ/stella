export type PluginScope = "system" | "system_agent" | "user" | "user_agent";

export interface ResourceFileInfo {
  path: string;
  size: number;
  is_executable: boolean;
  digest: string;
}

export interface PluginMCPServerSummary {
  server_key: string;
  transport?: "streamable_http" | "sse";
  auth_type?: "none" | "bearer" | "oauth";
  credential_mode?: "shared" | "per_user";
  endpoint_configured: boolean;
  bearer_configured: boolean;
  oauth_client_id_configured: boolean;
  oauth_client_secret_configured: boolean;
}

export interface PluginResource {
  id: string;
  name: string;
  display_name: string;
  version: string;
  description: string;
  scope: PluginScope;
  user_id?: string;
  agent_id?: string;
  content_digest: string;
  settings_digest: string;
  is_enabled: boolean;
  is_forbidden: boolean;
  is_read_only: boolean;
  is_overridden: boolean;
  diagnostics: Array<{
    severity: "info" | "warning" | "error";
    code: string;
    path?: string;
    message: string;
  }>;
  files: ResourceFileInfo[];
  resource_summary: {
    binaries: Array<{ name: string; version: string; }>;
    skills: Array<{ name: string; }>;
    session_env: Array<{ env_var: string; source: string; required: boolean; }>;
    oauth_provider_configured: boolean;
    mcp_servers: PluginMCPServerSummary[];
  };
}

export interface McpDeclaration {
  url: string;
  transport: "streamable_http" | "sse";
  description?: string;
  headers?: Record<string, string>;
  call_timeout_seconds?: number;
  auth_type: "none" | "bearer" | "oauth";
  credential_mode: "shared" | "per_user";
  credential_ref?: string;
  client_id?: string;
  client_secret_ref?: string;
  token_endpoint_auth_method?:
    | "client_secret_basic"
    | "client_secret_post"
    | "none";
  scopes?: string[];
}

export interface McpServer {
  id: string;
  resource_id: string;
  server_key: string;
  name: string;
  scope: PluginScope;
  user_id?: string;
  agent_id?: string;
  content_digest: string;
  settings_digest: string;
  is_enabled: boolean;
  is_read_only: boolean;
  is_standalone: boolean;
  is_overridden: boolean;
  diagnostics: Array<{
    severity: "info" | "warning" | "error";
    code: string;
    message: string;
  }>;
  declaration: McpDeclaration | null;
  needs_auth: boolean;
  status: "unknown" | "ready" | "needs_auth" | "error";
  status_error?: string;
  tools: Array<{ name: string; }>;
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
