export interface OAuthState {
  connected: boolean;
  needs_reconnect: boolean;
  client_registered: boolean;
}
export interface McpServer {
  id: string;
  name: string;
  url: string;
  status: string;
  status_error?: string;
  probed_at?: string;
  version: string;
  tools?: { name: string; }[];
  scope?: string;
  credential_mode?: string;
  oauth?: OAuthState;
  [key: string]: unknown;
}
export interface AgentMcpServer extends McpServer {
  agent_id?: string;
  enabled?: boolean;
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
export interface Server extends McpServer {
  credential_mode: string;
  oauth?: OAuthState;
}
