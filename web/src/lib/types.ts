export interface Session {
  id: string;
  title: string;
  channel: string;
  agent_id: string;
  agent_name: string;
  user_id: number;
  user_name: string;
  last_active: string;
  archived: boolean;
}

export interface ToolBlock {
  type: "tool_call";
  id: string;
  name: string;
  arguments: Record<string, unknown>;
  status?: "running" | "done";
  result?: ToolResult;
}

export interface TextBlock {
  type: "text";
  text: string;
}

export interface ThinkingBlock {
  type: "thinking";
  thinking?: string;
  redacted?: boolean;
}

export type ContentBlock = TextBlock | ThinkingBlock | ToolBlock;

export interface ToolResult {
  tool_call_id: string;
  content: string;
  is_error: boolean;
}

export interface Message {
  role: "user" | "assistant" | "tool";
  content?: string;
  blocks?: ContentBlock[];
  tool_call_id?: string;
  timestamp: string;
  token_count?: number;
  model?: string;
  _streaming?: boolean;
}

export interface Agent {
  id: string;
  name: string;
}

export interface AgentDetail extends Agent {
  model: string;
  model_strong: string;
  model_fast: string;
  system_prompt: string;
  soul: string;
  scope: "system" | "restricted";
  enabled: boolean;
  creator_id: number;
  sandbox: AgentSandbox;
  template_id?: string;
}

export interface AgentSandbox {
  network: {
    mode: "disabled" | "allow_all" | "whitelist";
    allowlist: string[];
  };
}

export interface Identity {
  id: number;
  platform: string;
  external_id: string;
  name: string;
  linked_at: string;
}

export interface User {
  id: number;
  username: string;
  role: string;
  is_active: boolean;
  created_at: string;
  updated_at: string;
  identities: Identity[];
  notify_identity_id: number | null;
  default_agent_id: string;
}

export interface UserMemory {
  agent_id: string;
  content: string;
  updated_at: string;
}

export interface BuiltinItem {
  id: string;
  name: string;
  description: string;
  content?: string;
  metadata?: Record<string, unknown>;
}

export interface Skill {
  id: string;
  name: string;
  description: string;
  status: "active" | "draft" | "deprecated";
  scope: "system" | "user" | "agent";
  disable_model_invocation: boolean;
  files?: string[];
}

export interface SkillSearchResult {
  id: string;
  skillId: string;
  source: string;
  name: string;
  description: string;
  installs: number;
}

export interface Personalisation {
  soul: string;
  soulDraft: string;
  profile: string;
  profileDraft: string;
  loaded: boolean;
}

export interface Tool {
  name: string;
  description: string;
  category: string;
  input_schema: Record<string, unknown>;
}

export interface Workspace {
  paths: string[];
}

export interface SchedulerJob {
  id: number;
  name: string;
  cron: string;
  every: string;
  at: string;
  message: string;
  session_mode: string;
  enabled: boolean;
  agent_id: string;
  user_id: number;
  owner_kind: string;
  plugin_id: string;
  job_key: string;
  runtime_name: string;
  description: string;
  payload: Record<string, unknown>;
  last_run_at: string;
  last_error: string;
}

export interface SchedulerJobRun {
  id: number;
  status: string;
  started_at: string;
  duration: string;
  session_id: string;
  error: string;
}

export interface SchedulerJobList {
  items: SchedulerJob[];
}

// Plugin types
export interface Plugin {
  id: string;
  kind: string;
  name: string;
  display_name: string;
  description: string;
  enabled: boolean;
  config: Record<string, unknown>;
  capabilities: string[];
  has_config: boolean;
  has_status: boolean;
  managed?: boolean;
  supports_notifications?: boolean;
}

export interface ManifestBinary {
  name: string;
  repo: string;
  version?: string;
  bin_path?: string;
  exe?: string;
}

export interface ManifestSessionEnv {
  env_var: string;
  source: string;
  value?: string;
  required?: boolean;
}

export interface ManifestPlugin {
  id: string;
  kind: string;
  name: string;
  display_name: string;
  description: string;
  enabled: boolean;
  binaries?: ManifestBinary[];
  session_env?: ManifestSessionEnv[];
  oauth_provider?: string;
  oauth_provider_config_field?: string;
  oauth_provider_choices?: string[];
}

export interface McpServer {
  id: number;
  expanded: boolean;
  name: string;
  enabled: boolean;
  transport: string;
  command: string;
  url: string;
  timeout_seconds: number;
  args: { id: number; value: string }[];
  env: { id: number; key: string; value: string }[];
  headers: { id: number; key: string; value: string }[];
}

export interface McpStatus {
  name: string;
  state: string;
  suppressed: boolean;
  discovered_tool_count: number;
  failures: number;
  last_connected_at: string;
  last_error?: string;
}

export interface PluginSchema {
  properties?: Record<string, PluginSchemaProperty>;
}

export interface PluginSchemaProperty {
  type?: string | string[];
  description?: string;
  default?: unknown;
  enum?: unknown[];
}

export interface PluginSchemaField {
  name: string;
  schema: PluginSchemaProperty;
}

export interface PluginWithMeta extends Plugin {
  _manifest: boolean;
  _manifestPlugin?: ManifestPlugin | null;
}

export interface ProviderType {
  id: string;
  name: string;
  default_url: string;
}

export interface ModelCost {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
}

export interface ModelConfig {
  id: string;
  name: string;
  enabled: boolean;
  reasoning: boolean;
  input: string[];
  output: string[];
  contextWindow?: number;
  maxTokens?: number;
  cost?: ModelCost;
}

export interface Provider {
  id: string;
  type: string;
  name: string;
  enabled: boolean;
  api_key: string;
  base_url: string;
  models: Record<string, ModelConfig>;
}

export interface ProviderModel {
  id: string;
  name: string;
  enabled: boolean;
  source: string;
}

export interface CustomModelForm {
  original_id: string;
  id: string;
  name: string;
  enabled: boolean;
  reasoning: boolean;
  input: string;
  output: string;
  context_window: string;
  max_tokens: string;
  cost_input: string;
  cost_output: string;
  cost_cache_read: string;
  cost_cache_write: string;
}

export interface VaultEntry {
  name: string;
  created_at: string;
  updated_at: string;
}

export interface OAuthProvider {
  provider: string;
}

export interface OAuthFlow {
  flow_id: string;
  verification_uri: string;
  user_code: string;
}

export interface Channel {
  id: string;
  type: string;
  label: string;
  agent_id: string;
  agent_name: string;
  enabled: boolean;
  config: string;
  _config?: Record<string, unknown>;
}
