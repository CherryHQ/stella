import type {
  ChannelIdentity as SdkChannelIdentity,
  ComponentsAgent,
  ComponentsAgentTool,
  ComponentsAuthUser,
  ComponentsBuiltinResourceDetail,
  ComponentsChannel,
  ComponentsIdentity,
  ComponentsJob,
  ComponentsOAuthFlowStatus,
  ComponentsOAuthProviderConfig,
  ComponentsOAuthProviderStatus,
  ComponentsPluginView,
  ComponentsProvider,
  ComponentsProviderModelItem,
  ComponentsProviderType,
  ComponentsSkill,
  ComponentsSkillSearchResult,
  ComponentsUserMemory,
  ComponentsVaultEntry,
  JobRun,
  Project as SdkProject,
  SessionWorkspace,
} from "@/lib/api-client/types.gen";

// ── SDK re-exports ────────────────────────────────────────────────────────────
// Fields marked optional in OpenAPI (for create/update) but always present in
// GET responses are overridden to required via intersection.

export type Agent = ComponentsAgent & { id: string; name: string };
export type Session = import("@/lib/api-client/types.gen").ComponentsSession;
export type SessionDetail = import("@/lib/api-client/types.gen").ComponentsSessionDetail;
export type Identity = ComponentsIdentity & { id: string };
export type ChannelIdentity = SdkChannelIdentity & { id: string };
export type Skill = ComponentsSkill & {
  id: string;
  scope: string;
  name: string;
  description: string;
  status: string;
  disable_model_invocation: boolean;
};
export type Tool = ComponentsAgentTool;
export type SchedulerJob = ComponentsJob;
export type SchedulerJobRun = JobRun;
export type BuiltinItem = ComponentsBuiltinResourceDetail;
export type SkillSearchResult = ComponentsSkillSearchResult & {
  description?: string;
};
export type VaultEntry = ComponentsVaultEntry;
export type OAuthProviderConfig = ComponentsOAuthProviderConfig;
export type ProviderType = ComponentsProviderType;
export type Provider = ComponentsProvider;
export type ProviderModel = ComponentsProviderModelItem;
export type Workspace = SessionWorkspace;
export type Project = SdkProject;
export type OAuthFlow = ComponentsOAuthFlowStatus;

// ── SDK extensions (SDK type + UI-only fields) ────────────────────────────────

export type AgentDetail = ComponentsAgent & {
  id: string;
  name: string;
  sandbox: AgentSandbox;
  template_id?: string;
  _highlight?: boolean;
};

export type Channel = ComponentsChannel & {
  _config?: Record<string, unknown>;
};

export type User = ComponentsAuthUser & {
  notify_identity_id?: string | null;
  default_agent_id?: string;
};

export type OAuthProvider = ComponentsOAuthProviderStatus & {
  icon?: string;
};

export type Plugin = ComponentsPluginView & {
  id: string;
  kind: string;
  name: string;
  enabled: boolean;
  capabilities: Array<string>;
  has_config: boolean;
  has_status: boolean;
  supports_notifications?: boolean;
  env_locked?: boolean;
};

// ── Local types (no SDK equivalent) ───────────────────────────────────────────

export interface ToolBlock {
  type: "tool_call";
  id: string;
  name?: string;
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

/**
 * A renderable reference an agent emitted when it created (or referenced) a
 * Stella entity via a CLI tool. `type` + `id` are load-bearing; `preview` is a
 * loading placeholder only — cards hydrate the live entity by id and never
 * trust it past first paint.
 */
export interface RenderableReference {
  v: 1;
  type: "task" | "goal" | "recally_article" | (string & {});
  id: string;
  intent?: "created" | "referenced";
  preview?: { title?: string; status?: string };
}

export interface ToolResult {
  tool_call_id: string;
  content: string;
  is_error: boolean;
  references?: RenderableReference[];
}

export interface Message {
  id?: string;
  role: "user" | "assistant" | "tool";
  content?: string;
  blocks?: ContentBlock[];
  tool_call_id?: string;
  references?: RenderableReference[];
  timestamp: string;
  token_count?: number;
  model?: string;
  streaming?: boolean;
}

export interface AgentSandbox {
  network: {
    mode: "disabled" | "allow_all" | "whitelist";
    allowlist: string[];
  };
}

export type UserMemory = ComponentsUserMemory & {
  agent_id: string;
  content: string;
  soul: string;
  version: number;
  constraints: string;
  updated_at: string;
};

export interface Personalisation {
  soul: string;
  soulDraft: string;
  profile: string;
  profileDraft: string;
  loaded: boolean;
}

export interface ManifestBinary {
  name: string;
  tool: string;
  version?: string;
  bin_path?: string;
  bin?: string;
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
  category?: string;
  essential?: boolean;
  prompt?: string;
  binaries?: ManifestBinary[];
  session_env?: ManifestSessionEnv[];
  oauth_provider?: string;
}

export interface ManifestOAuthProvider {
  id: string;
  scopes?: string[];
  vault_key?: string;
  client_id?: string;
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
