package config

import (
	"context"
)

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
	CreateChannel(ctx context.Context, ch Channel) error
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
