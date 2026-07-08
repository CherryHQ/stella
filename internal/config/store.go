package config

import (
	"context"
	"time"
)

type ProviderModelCost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
}

type ProviderModel struct {
	ID            string            `json:"id,omitempty"`
	Name          string            `json:"name,omitempty"`
	Enabled       bool              `json:"enabled"`
	Reasoning     bool              `json:"reasoning,omitempty"`
	Input         []string          `json:"input,omitempty"`
	Output        []string          `json:"output,omitempty"`
	ContextWindow int               `json:"contextWindow,omitempty"`
	MaxTokens     int               `json:"maxTokens,omitempty"`
	Cost          ProviderModelCost `json:"cost,omitzero"`
}

// Provider represents an LLM API provider.
type Provider struct {
	ID      string                   `json:"id"`
	Type    string                   `json:"type"`
	Name    string                   `json:"name"`
	Enabled bool                     `json:"enabled"`
	APIKey  string                   `json:"api_key"`
	BaseURL string                   `json:"base_url"`
	Models  map[string]ProviderModel `json:"models,omitempty"`
}

// AgentScope constants define the access scope for an agent.
const (
	AgentScopeSystem     = "system"     // all users can access
	AgentScopeRestricted = "restricted" // only assigned users can access
)

// Agent represents an agent definition.
// Model fields use {provider}/{model} format (e.g. "anthropic/claude-sonnet-4-6").
type Agent struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	Model               string        `json:"model"`
	ModelThinking       string        `json:"model_thinking"`
	ModelStrong         string        `json:"model_strong"`
	ModelStrongThinking string        `json:"model_strong_thinking"`
	ModelFast           string        `json:"model_fast"`
	ModelFastThinking   string        `json:"model_fast_thinking"`
	SystemPrompt        string        `json:"system_prompt"`
	Soul                string        `json:"soul"`
	Workspace           string        `json:"workspace"`
	Sandbox             SandboxConfig `json:"sandbox"`
	Scope               string        `json:"scope"`
	CreatorID           string        `json:"creator_id"`
	Enabled             bool          `json:"enabled"`
	LastActive          *time.Time    `json:"last_active,omitempty"`
}

// Channel represents a platform channel configuration.
type Channel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	AgentID string `json:"agent_id,omitempty"`
	Enabled bool   `json:"enabled"`
	Config  string `json:"config"`
}

// Store provides typed access to configuration stored in the database.
type Store interface {
	// Providers
	ListProviders(ctx context.Context) ([]Provider, error)
	GetProvider(ctx context.Context, id string) (Provider, error)
	CreateProvider(ctx context.Context, p Provider) error
	UpdateProvider(ctx context.Context, p Provider) error
	DeleteProvider(ctx context.Context, id string) error

	// Fetched-model cache — the model IDs an admin pulled from each provider's
	// API. ListCachedModels returns a flat list across providers; ReplaceCachedModels
	// replaces the fetched set for one provider only, leaving others untouched.
	ListCachedModels(ctx context.Context) ([]CachedModel, error)
	ReplaceCachedModels(ctx context.Context, providerID string, modelIDs []string) error

	// Agents
	ListAgents(ctx context.Context) ([]Agent, error)
	ListEnabledAgents(ctx context.Context) ([]Agent, error)
	ListAccessibleAgents(ctx context.Context, userID string) ([]Agent, error)
	GetAgent(ctx context.Context, id string) (Agent, error)
	CreateAgent(ctx context.Context, a Agent) error
	UpdateAgent(ctx context.Context, a Agent) error
	DeleteAgent(ctx context.Context, id string) error

	// Channels
	ListChannels(ctx context.Context) ([]Channel, error)
	ListChannelsByType(ctx context.Context, channelType string) ([]Channel, error)
	GetChannel(ctx context.Context, id string) (Channel, error)
	UpsertChannel(ctx context.Context, ch Channel) error
	DeleteChannel(ctx context.Context, id string) error

	// Manifest plugin overrides — tunables for manifest-declared plugins.
	// Reads merge the builtin manifest defaults with these rows.
	GetManifestPluginOverride(ctx context.Context, pluginID string) (ManifestPluginOverride, bool, error)
	ListManifestPluginOverrides(ctx context.Context) ([]ManifestPluginOverride, error)
	UpsertManifestPluginOverride(ctx context.Context, override ManifestPluginOverride) error
	DeleteManifestPluginOverride(ctx context.Context, pluginID string) error

	// Plugins — read paths merge BuiltinPlugins() with DB override rows.
	ListPlugins(ctx context.Context) ([]Plugin, error)
	ListPluginOverrides(ctx context.Context) ([]Plugin, error)
	ListPluginsByKind(ctx context.Context, kind string) ([]Plugin, error)
	ListEnabledPlugins(ctx context.Context) ([]Plugin, error)
	GetPlugin(ctx context.Context, id string) (Plugin, error)
	UpsertPlugin(ctx context.Context, p Plugin) error
	SetPluginEnabled(ctx context.Context, id string, enabled bool) error
	SetPluginConfig(ctx context.Context, id string, config map[string]any) error
	DeletePlugin(ctx context.Context, id string) error

	// Chat Agents (group -> agent mapping)
	GetChatAgent(ctx context.Context, channelID, platform, chatID string) (string, error) // returns agentID
	SetChatAgent(ctx context.Context, channelID, platform, chatID, agentID string) error
	DeleteChatAgent(ctx context.Context, channelID, platform, chatID string) error

	// Settings (key-value JSON)
	GetSetting(ctx context.Context, key string) (string, error) // returns JSON string
	SetSetting(ctx context.Context, key, value string) error

	// Snapshot assembles a read-only config snapshot for an agent.
	Snapshot(ctx context.Context, agentID string) (*Snapshot, error)

	// Bootstrap seeds default config for a fresh install.
	Seed(ctx context.Context) error
}
