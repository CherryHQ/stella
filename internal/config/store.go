package config

import "context"

// Provider represents an LLM API provider.
type Provider struct {
	ID      string
	Name    string
	APIKey  string
	BaseURL string
}

// Agent represents an agent definition.
type Agent struct {
	ID           string
	Name         string
	ProviderID   string
	Model        string
	ModelStrong  string
	ModelFast    string
	SystemPrompt string
	Workspace    string
	Enabled      bool
}

// Channel represents a platform channel configuration.
type Channel struct {
	ID      string
	Enabled bool
	Config  string // JSON blob
}

// User represents a platform user.
type User struct {
	ID             int64
	ExternalID     string
	Platform       string
	Name           string
	DefaultAgentID string
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

	// Users
	ListUsers(ctx context.Context) ([]User, error)
	GetUser(ctx context.Context, id int64) (User, error)
	UpsertUser(ctx context.Context, externalID, platform, name string) (User, error)
	UpdateUserDefaultAgent(ctx context.Context, userID int64, agentID string) error

	// Chat Agents (group -> agent mapping)
	GetChatAgent(ctx context.Context, platform, chatID string) (string, error) // returns agentID
	SetChatAgent(ctx context.Context, platform, chatID, agentID string) error
	DeleteChatAgent(ctx context.Context, platform, chatID string) error

	// User Agent Memory
	GetUserAgentMemory(ctx context.Context, userID int64, agentID string) (string, error)
	SetUserAgentMemory(ctx context.Context, userID int64, agentID, content string) error

	// Settings (key-value JSON)
	GetSetting(ctx context.Context, key string) (string, error) // returns JSON string
	SetSetting(ctx context.Context, key, value string) error

	// Snapshot assembles a read-only config snapshot for an agent.
	Snapshot(ctx context.Context, agentID string) (*Snapshot, error)

	// Bootstrap
	SeedDefaults(ctx context.Context) error
}
