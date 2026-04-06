package config

import "context"

// Provider represents an LLM API provider.
type Provider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
}

// AgentScope constants define the access scope for an agent.
const (
	AgentScopeSystem     = "system"     // all users can access
	AgentScopeRestricted = "restricted" // only assigned users can access
)

// Agent represents an agent definition.
// Model fields use {provider}/{model} format (e.g. "anthropic/claude-sonnet-4-6").
type Agent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Model        string `json:"model"`
	ModelStrong  string `json:"model_strong"`
	ModelFast    string `json:"model_fast"`
	SystemPrompt string `json:"system_prompt"`
	Workspace    string `json:"workspace"`
	Scope        string `json:"scope"`
	CreatorID    int64  `json:"creator_id"`
	Enabled      bool   `json:"enabled"`
}

// Channel represents a platform channel configuration.
type Channel struct {
	ID      string `json:"id"`
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

	// Agents
	ListAgents(ctx context.Context) ([]Agent, error)
	ListEnabledAgents(ctx context.Context) ([]Agent, error)
	GetAgent(ctx context.Context, id string) (Agent, error)
	CreateAgent(ctx context.Context, a Agent) error
	UpdateAgent(ctx context.Context, a Agent) error
	DeleteAgent(ctx context.Context, id string) error

	// Channels
	ListChannels(ctx context.Context) ([]Channel, error)
	GetChannel(ctx context.Context, id string) (Channel, error)
	UpsertChannel(ctx context.Context, ch Channel) error

	// Plugins
	ListPlugins(ctx context.Context) ([]Plugin, error)
	ListPluginsByKind(ctx context.Context, kind string) ([]Plugin, error)
	ListEnabledPlugins(ctx context.Context) ([]Plugin, error)
	GetPlugin(ctx context.Context, id string) (Plugin, error)
	UpsertPlugin(ctx context.Context, p Plugin) error
	SetPluginEnabled(ctx context.Context, id string, enabled bool) error
	SetPluginConfig(ctx context.Context, id string, config map[string]any) error
	DeletePlugin(ctx context.Context, id string) error

	// Chat Agents (group -> agent mapping)
	GetChatAgent(ctx context.Context, platform, chatID string) (string, error) // returns agentID
	SetChatAgent(ctx context.Context, platform, chatID, agentID string) error
	DeleteChatAgent(ctx context.Context, platform, chatID string) error

	// Settings (key-value JSON)
	GetSetting(ctx context.Context, key string) (string, error) // returns JSON string
	SetSetting(ctx context.Context, key, value string) error

	// Snapshot assembles a read-only config snapshot for an agent.
	Snapshot(ctx context.Context, agentID string) (*Snapshot, error)

	// Bootstrap
	SeedDefaults(ctx context.Context) error
}
