package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vaayne/anna/pkg/db/sqlc"
)

// DBStore implements Store using sqlc queries backed by SQLite.
type DBStore struct {
	q *sqlc.Queries
}

// NewDBStore creates a new DBStore wrapping the given database connection.
// It sets MaxOpenConns(1) to mitigate SQLite concurrency issues.
func NewDBStore(db *sql.DB) *DBStore {
	db.SetMaxOpenConns(1)
	return &DBStore{q: sqlc.New(db)}
}

// --- Providers (backed by settings_providers) ---

func (s *DBStore) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.q.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	out := make([]Provider, len(rows))
	for i, r := range rows {
		out[i] = providerFromDB(r)
		applyProviderEnvFallback(&out[i])
	}
	return out, nil
}

func (s *DBStore) GetProvider(ctx context.Context, id string) (Provider, error) {
	r, err := s.q.GetProvider(ctx, id)
	if err != nil {
		return Provider{}, fmt.Errorf("get provider %q: %w", id, err)
	}
	p := providerFromDB(r)
	applyProviderEnvFallback(&p)
	return p, nil
}

func (s *DBStore) CreateProvider(ctx context.Context, p Provider) error {
	configJSON, err := json.Marshal(providerConfig(p))
	if err != nil {
		return fmt.Errorf("create provider %q: marshal config: %w", p.ID, err)
	}
	enabled := int64(1)
	if _, err := s.q.CreateProvider(ctx, sqlc.CreateProviderParams{
		ID:      p.ID,
		Type:    providerType(p),
		Name:    providerName(p),
		Enabled: enabled,
		Config:  string(configJSON),
	}); err != nil {
		return fmt.Errorf("create provider %q: %w", p.ID, err)
	}
	return nil
}

func (s *DBStore) UpdateProvider(ctx context.Context, p Provider) error {
	configJSON, err := json.Marshal(providerConfig(p))
	if err != nil {
		return fmt.Errorf("update provider %q: marshal config: %w", p.ID, err)
	}
	enabled := int64(0)
	if p.Enabled {
		enabled = 1
	}
	if err := s.q.UpdateProvider(ctx, sqlc.UpdateProviderParams{
		Type:    providerType(p),
		Name:    providerName(p),
		Enabled: enabled,
		Config:  string(configJSON),
		ID:      p.ID,
	}); err != nil {
		return fmt.Errorf("update provider %q: %w", p.ID, err)
	}
	return nil
}

func (s *DBStore) DeleteProvider(ctx context.Context, id string) error {
	return s.q.DeleteProvider(ctx, id)
}

// --- Agents ---

func (s *DBStore) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.q.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	out := make([]Agent, len(rows))
	for i, r := range rows {
		agent, err := agentFromDB(r)
		if err != nil {
			return nil, fmt.Errorf("list agents: %w", err)
		}
		out[i] = agent
	}
	return out, nil
}

func (s *DBStore) ListEnabledAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.q.ListEnabledAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled agents: %w", err)
	}
	out := make([]Agent, len(rows))
	for i, r := range rows {
		agent, err := agentFromDB(r)
		if err != nil {
			return nil, fmt.Errorf("list enabled agents: %w", err)
		}
		out[i] = agent
	}
	return out, nil
}

func (s *DBStore) GetAgent(ctx context.Context, id string) (Agent, error) {
	r, err := s.q.GetAgent(ctx, id)
	if err != nil {
		return Agent{}, fmt.Errorf("get agent %q: %w", id, err)
	}
	agent, err := agentFromDB(r)
	if err != nil {
		return Agent{}, fmt.Errorf("get agent %q: %w", id, err)
	}
	return agent, nil
}

func (s *DBStore) CreateAgent(ctx context.Context, a Agent) error {
	enabled := int64(0)
	if a.Enabled {
		enabled = 1
	}
	scope := a.Scope
	if scope == "" {
		scope = AgentScopeSystem
	}
	if err := a.Sandbox.Validate(); err != nil {
		return fmt.Errorf("create agent %q: %w", a.ID, err)
	}
	sandboxJSON, err := marshalSandboxConfig(a.Sandbox)
	if err != nil {
		return fmt.Errorf("create agent %q: %w", a.ID, err)
	}
	_, err = s.q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID:           a.ID,
		Name:         a.Name,
		Model:        a.Model,
		ModelStrong:  a.ModelStrong,
		ModelFast:    a.ModelFast,
		SystemPrompt: a.SystemPrompt,
		Workspace:    a.Workspace,
		Sandbox:      sandboxJSON,
		Scope:        scope,
		CreatorID:    a.CreatorID,
		Enabled:      enabled,
	})
	if err != nil {
		return fmt.Errorf("create agent %q: %w", a.ID, err)
	}
	return nil
}

func (s *DBStore) UpdateAgent(ctx context.Context, a Agent) error {
	enabled := int64(0)
	if a.Enabled {
		enabled = 1
	}
	scope := a.Scope
	if scope == "" {
		scope = AgentScopeSystem
	}
	if err := a.Sandbox.Validate(); err != nil {
		return fmt.Errorf("update agent %q: %w", a.ID, err)
	}
	sandboxJSON, err := marshalSandboxConfig(a.Sandbox)
	if err != nil {
		return fmt.Errorf("update agent %q: %w", a.ID, err)
	}
	err = s.q.UpdateAgent(ctx, sqlc.UpdateAgentParams{
		ID:           a.ID,
		Name:         a.Name,
		Model:        a.Model,
		ModelStrong:  a.ModelStrong,
		ModelFast:    a.ModelFast,
		SystemPrompt: a.SystemPrompt,
		Workspace:    a.Workspace,
		Sandbox:      sandboxJSON,
		Scope:        scope,
		Enabled:      enabled,
	})
	if err != nil {
		return fmt.Errorf("update agent %q: %w", a.ID, err)
	}
	return nil
}

func (s *DBStore) DeleteAgent(ctx context.Context, id string) error {
	return s.q.DeleteAgent(ctx, id)
}

// --- Channels ---

func (s *DBStore) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.q.ListChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	out := make([]Channel, len(rows))
	for i, r := range rows {
		out[i] = channelFromDB(r)
	}
	return out, nil
}

func (s *DBStore) ListChannelsByType(ctx context.Context, channelType string) ([]Channel, error) {
	rows, err := s.q.ListChannelsByType(ctx, channelType)
	if err != nil {
		return nil, fmt.Errorf("list %s channels: %w", channelType, err)
	}
	out := make([]Channel, len(rows))
	for i, r := range rows {
		out[i] = channelFromDB(r)
	}
	return out, nil
}

func (s *DBStore) GetChannel(ctx context.Context, id string) (Channel, error) {
	r, err := s.q.GetChannel(ctx, id)
	if err != nil {
		return Channel{}, fmt.Errorf("get channel %q: %w", id, err)
	}
	return channelFromDB(r), nil
}

func (s *DBStore) UpsertChannel(ctx context.Context, ch Channel) error {
	channelType := ch.Type
	if channelType == "" {
		channelType = ch.ID
	}
	return s.q.UpsertChannel(ctx, sqlc.UpsertChannelParams{
		ID:      ch.ID,
		Type:    channelType,
		AgentID: sql.NullString{String: ch.AgentID, Valid: ch.AgentID != ""},
		Enabled: boolToInt64(ch.Enabled),
		Config:  ch.Config,
	})
}

func (s *DBStore) DeleteChannel(ctx context.Context, id string) error {
	return s.q.DeleteChannel(ctx, id)
}

// --- Plugins ---

func (s *DBStore) ListPlugins(ctx context.Context) ([]Plugin, error) {
	rows, err := s.q.ListPlugins(ctx)
	if err != nil {
		return nil, fmt.Errorf("list plugins: %w", err)
	}
	out := make([]Plugin, len(rows))
	for i, r := range rows {
		out[i] = pluginFromDB(r)
	}
	return out, nil
}

func (s *DBStore) ListPluginsByKind(ctx context.Context, kind string) ([]Plugin, error) {
	rows, err := s.q.ListPluginsByKind(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("list plugins by kind %q: %w", kind, err)
	}
	out := make([]Plugin, len(rows))
	for i, r := range rows {
		out[i] = pluginFromDB(r)
	}
	return out, nil
}

func (s *DBStore) ListEnabledPlugins(ctx context.Context) ([]Plugin, error) {
	rows, err := s.q.ListEnabledPlugins(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled plugins: %w", err)
	}
	out := make([]Plugin, len(rows))
	for i, r := range rows {
		out[i] = pluginFromDB(r)
	}
	return out, nil
}

func (s *DBStore) GetPlugin(ctx context.Context, id string) (Plugin, error) {
	r, err := s.q.GetPlugin(ctx, id)
	if err != nil {
		return Plugin{}, fmt.Errorf("get plugin %q: %w", id, err)
	}
	return pluginFromDB(r), nil
}

func (s *DBStore) UpsertPlugin(ctx context.Context, p Plugin) error {
	configJSON, err := json.Marshal(p.Config)
	if err != nil {
		return fmt.Errorf("marshal plugin config %q: %w", p.ID, err)
	}
	enabled := int64(0)
	if p.Enabled {
		enabled = 1
	}
	return s.q.UpsertPlugin(ctx, sqlc.UpsertPluginParams{
		ID:      p.ID,
		Kind:    p.Kind,
		Name:    p.Name,
		Enabled: enabled,
		Config:  string(configJSON),
	})
}

func (s *DBStore) SetPluginEnabled(ctx context.Context, id string, enabled bool) error {
	v := int64(0)
	if enabled {
		v = 1
	}
	return s.q.UpdatePluginEnabled(ctx, sqlc.UpdatePluginEnabledParams{
		ID:      id,
		Enabled: v,
	})
}

func (s *DBStore) SetPluginConfig(ctx context.Context, id string, config map[string]any) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal plugin config %q: %w", id, err)
	}
	return s.q.UpdatePluginConfig(ctx, sqlc.UpdatePluginConfigParams{
		ID:     id,
		Config: string(configJSON),
	})
}

func (s *DBStore) DeletePlugin(ctx context.Context, id string) error {
	return s.q.DeletePlugin(ctx, id)
}

// --- Chat Agents ---

func (s *DBStore) GetChatAgent(ctx context.Context, channelID, platform, chatID string) (string, error) {
	if channelID == "" {
		channelID = platform
	}
	r, err := s.q.GetChatAgent(ctx, sqlc.GetChatAgentParams{
		ChannelID: channelID,
		Platform:  platform,
		ChatID:    chatID,
	})
	if err != nil {
		r, err = s.q.GetLegacyChatAgent(ctx, sqlc.GetLegacyChatAgentParams{
			Platform: platform,
			ChatID:   chatID,
		})
		if err != nil {
			return "", fmt.Errorf("get chat agent: %w", err)
		}
	}
	return r.AgentID, nil
}

func (s *DBStore) SetChatAgent(ctx context.Context, channelID, platform, chatID, agentID string) error {
	if channelID == "" {
		channelID = platform
	}
	return s.q.UpsertChatAgent(ctx, sqlc.UpsertChatAgentParams{
		ChannelID: channelID,
		Platform:  platform,
		ChatID:    chatID,
		AgentID:   agentID,
	})
}

func (s *DBStore) DeleteChatAgent(ctx context.Context, channelID, platform, chatID string) error {
	if channelID == "" {
		channelID = platform
	}
	return s.q.DeleteChatAgent(ctx, sqlc.DeleteChatAgentParams{
		ChannelID: channelID,
		Platform:  platform,
		ChatID:    chatID,
	})
}

// --- Settings ---

func (s *DBStore) GetSetting(ctx context.Context, key string) (string, error) {
	r, err := s.q.GetSetting(ctx, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("get setting %q: %w", key, err)
	}
	return r.Value, nil
}

func (s *DBStore) SetSetting(ctx context.Context, key, value string) error {
	return s.q.UpsertSetting(ctx, sqlc.UpsertSettingParams{
		Key:   key,
		Value: value,
	})
}

// --- Snapshot ---

func (s *DBStore) Snapshot(ctx context.Context, agentID string) (*Snapshot, error) {
	ag, err := s.q.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("snapshot: get agent %q: %w", agentID, err)
	}

	// Load plugins once for non-provider plugin state exposed in the snapshot.
	pluginRows, err := s.q.ListPlugins(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot: list plugins: %w", err)
	}
	plugins := make([]Plugin, len(pluginRows))
	for i, r := range pluginRows {
		plugins[i] = pluginFromDB(r)
	}

	provIDs := collectProviderIDs(ag.Model, ag.ModelStrong, ag.ModelFast)
	providerRows, err := s.q.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot: list providers: %w", err)
	}
	providerByID := make(map[string]Provider, len(providerRows))
	for _, row := range providerRows {
		provider := providerFromDB(row)
		applyProviderEnvFallback(&provider)
		providerByID[provider.ID] = provider
	}

	providers := make(map[string]ProviderCreds, len(provIDs))
	for _, pid := range provIDs {
		if provider, ok := providerByID[pid]; ok {
			providers[pid] = ProviderCreds{Type: provider.Type, APIKey: provider.APIKey, BaseURL: provider.BaseURL}
		}
	}

	defaultProvID, _ := ParseModelRef(ag.Model)
	defaultCreds := providers[defaultProvID]

	sandboxCfg, err := parseSandboxConfig(ag.Sandbox)
	if err != nil {
		return nil, fmt.Errorf("snapshot: parse agent sandbox config %q: %w", agentID, err)
	}

	snap := &Snapshot{
		AgentID:      agentID,
		Provider:     defaultProvID,
		Model:        ag.Model,
		ModelStrong:  ag.ModelStrong,
		ModelFast:    ag.ModelFast,
		Workspace:    ag.Workspace,
		Sandbox:      sandboxCfg,
		APIKey:       defaultCreds.APIKey,
		BaseURL:      defaultCreds.BaseURL,
		SystemPrompt: ag.SystemPrompt,
		Providers:    providers,
		Plugins:      plugins,
	}

	// Load settings.
	if val, err := s.GetSetting(ctx, "runner"); err == nil && val != "" {
		_ = json.Unmarshal([]byte(val), &snap.Runner)
	}
	if val, err := s.GetSetting(ctx, "compaction"); err == nil && val != "" {
		_ = json.Unmarshal([]byte(val), &snap.Compaction)
	}
	if val, err := s.GetSetting(ctx, "heartbeat"); err == nil && val != "" {
		_ = json.Unmarshal([]byte(val), &snap.Heartbeat)
	}
	if val, err := s.GetSetting(ctx, "scheduler"); err == nil && val != "" {
		_ = json.Unmarshal([]byte(val), &snap.Scheduler)
	}
	// Apply defaults.
	if snap.Runner.Type == "" {
		snap.Runner.Type = "go"
	}
	if snap.Runner.IdleTimeout == 0 {
		snap.Runner.IdleTimeout = 10
	}

	return snap, nil
}

// --- Bootstrap ---

// defaultAnnaSoul is the default system prompt for the anna agent.
const defaultAnnaSoul = `You are Anna — a sharp, efficient personal AI assistant.

- Warm but not chatty. Friendly but not performative.
- Lead with answers, not preamble.
- Match the user's energy: casual when they're casual, precise when they need precision.
- Own your mistakes quickly. No hedging or over-apologizing.
- Use humor sparingly and naturally — never forced.`

// SeedDefaults populates the DB with sensible defaults on first bootstrap.
// It is idempotent: if providers/agents already exist, it does nothing.
func (s *DBStore) SeedDefaults(ctx context.Context) error {
	// Seed default agent if none exist.
	agents, err := s.q.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("seed: list agents: %w", err)
	}
	if len(agents) == 0 {
		workspace := filepath.Join(AnnaHome(), "workspaces", "anna")
		sandboxJSON, err := marshalSandboxConfig(SandboxConfig{})
		if err != nil {
			return fmt.Errorf("seed: marshal anna sandbox config: %w", err)
		}
		_, err = s.q.CreateAgent(ctx, sqlc.CreateAgentParams{
			ID:           "anna",
			Name:         "Anna",
			Model:        "anthropic/claude-sonnet-4-6",
			SystemPrompt: defaultAnnaSoul,
			Workspace:    workspace,
			Sandbox:      sandboxJSON,
			Scope:        AgentScopeSystem,
			Enabled:      1,
		})
		if err != nil {
			return fmt.Errorf("seed: create anna agent: %w", err)
		}
	}

	// Seed plugins: migrate existing channels and seed all built-ins.
	if err := s.seedPlugins(ctx); err != nil {
		return err
	}
	if err := s.seedChannelInstances(ctx); err != nil {
		return err
	}
	if err := s.seedProviders(ctx); err != nil {
		return err
	}

	return nil
}

// seedPlugins seeds built-in plugin rows. Channel plugin rows only carry
// platform-level enablement; channel instance config lives in settings_channels.
func (s *DBStore) seedPlugins(ctx context.Context) error {
	// Seed all built-in plugins with INSERT OR IGNORE to preserve
	// user-modified state.
	if err := s.seedBuiltinPlugins(ctx, PluginKindTool, builtinToolNames, func(name string) int64 {
		if name == "mcp" {
			return 0
		}
		return 1
	}); err != nil {
		return err
	}
	if err := s.seedBuiltinPlugins(ctx, PluginKindChannel, builtinChannelNames, nil); err != nil {
		return err
	}
	if err := s.seedBuiltinPlugins(ctx, PluginKindHook, builtinHookNames, nil); err != nil {
		return err
	}
	if err := s.seedBuiltinPlugins(ctx, PluginKindMemory, builtinMemoryNames, func(name string) int64 {
		if name == "simple" {
			return 0 // simple is opt-in; lcm is the default
		}
		return 1
	}); err != nil {
		return err
	}

	// Seed the reflect plugin (conversation review).
	if err := s.q.SeedPlugin(ctx, sqlc.SeedPluginParams{
		ID:      "reflect",
		Kind:    "reflect",
		Name:    "reflect",
		Enabled: 0,
		Config:  `{}`,
	}); err != nil {
		return fmt.Errorf("seed: plugin reflect: %w", err)
	}

	return nil
}

func (s *DBStore) seedChannelInstances(ctx context.Context) error {
	existingByID, missingTypeIDs, err := s.loadExistingChannels(ctx)
	if err != nil {
		return err
	}
	if err := s.backfillChannelTypes(ctx, existingByID, missingTypeIDs); err != nil {
		return err
	}

	pluginByName, err := s.loadChannelPlugins(ctx)
	if err != nil {
		return err
	}
	if err := s.migrateLegacyChannelPluginConfigs(ctx, existingByID, pluginByName); err != nil {
		return err
	}
	return s.seedDefaultChannelInstances(ctx, existingByID, pluginByName)
}

func (s *DBStore) loadExistingChannels(ctx context.Context) (map[string]Channel, []string, error) {
	rows, err := s.q.ListChannels(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("seed: list channel instances: %w", err)
	}
	existingByID := make(map[string]Channel, len(rows))
	missingTypeIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		ch := channelFromDB(row)
		existingByID[ch.ID] = ch
		if row.Type == "" {
			missingTypeIDs = append(missingTypeIDs, ch.ID)
		}
	}
	return existingByID, missingTypeIDs, nil
}

func (s *DBStore) backfillChannelTypes(ctx context.Context, channels map[string]Channel, ids []string) error {
	for _, id := range ids {
		ch := channels[id]
		if err := s.UpsertChannel(ctx, ch); err != nil {
			return fmt.Errorf("seed: backfill channel type %q: %w", ch.ID, err)
		}
	}
	return nil
}

func (s *DBStore) loadChannelPlugins(ctx context.Context) (map[string]Plugin, error) {
	rows, err := s.q.ListPluginsByKind(ctx, PluginKindChannel)
	if err != nil {
		return nil, fmt.Errorf("seed: list channel plugins: %w", err)
	}
	plugins := make(map[string]Plugin, len(rows))
	for _, row := range rows {
		plugin := pluginFromDB(row)
		plugins[plugin.Name] = plugin
	}
	return plugins, nil
}

func (s *DBStore) migrateLegacyChannelPluginConfigs(ctx context.Context, existingByID map[string]Channel, pluginByName map[string]Plugin) error {
	for _, plugin := range pluginByName {
		existing := existingByID[plugin.Name]
		migrated, err := s.migrateLegacyChannelPluginConfig(ctx, plugin, existing)
		if err != nil {
			return err
		}
		if migrated.ID != "" {
			existingByID[migrated.ID] = migrated
		}
	}
	return nil
}

func (s *DBStore) seedDefaultChannelInstances(ctx context.Context, existingByID map[string]Channel, pluginByName map[string]Plugin) error {
	for _, name := range builtinChannelNames {
		if _, ok := existingByID[name]; ok {
			continue
		}
		enabled := true
		if plugin, ok := pluginByName[name]; ok {
			enabled = plugin.Enabled
		}
		if err := s.UpsertChannel(ctx, Channel{
			ID:      name,
			Type:    name,
			Enabled: enabled,
			Config:  "{}",
		}); err != nil {
			return fmt.Errorf("seed: default channel instance %q: %w", name, err)
		}
	}
	return nil
}

func (s *DBStore) seedBuiltinPlugins(ctx context.Context, kind string, names []string, enabledFor func(string) int64) error {
	for _, name := range names {
		enabled := int64(1)
		if enabledFor != nil {
			enabled = enabledFor(name)
		}
		if err := s.q.SeedPlugin(ctx, sqlc.SeedPluginParams{
			ID:      PluginID(kind, name),
			Kind:    kind,
			Name:    name,
			Enabled: enabled,
			Config:  "{}",
		}); err != nil {
			return fmt.Errorf("seed: plugin %s/%s: %w", kind, name, err)
		}
	}
	return nil
}

func (s *DBStore) migrateLegacyChannelPluginConfig(ctx context.Context, plugin Plugin, existing Channel) (Channel, error) {
	if plugin.Kind != PluginKindChannel || plugin.Name == "" || len(plugin.Config) == 0 {
		return Channel{}, nil
	}

	migrated := existing
	if migrated.ID == "" {
		migrated = Channel{ID: plugin.Name, Type: plugin.Name, Enabled: plugin.Enabled}
	}
	if migrated.Config == "" || migrated.Config == "{}" {
		configJSON, err := json.Marshal(plugin.Config)
		if err != nil {
			return Channel{}, fmt.Errorf("seed: marshal channel %q config: %w", plugin.Name, err)
		}
		migrated.Config = string(configJSON)
		if err := s.UpsertChannel(ctx, migrated); err != nil {
			return Channel{}, fmt.Errorf("seed: migrate channel plugin config %q: %w", plugin.Name, err)
		}
	}
	if err := s.SetPluginConfig(ctx, plugin.ID, map[string]any{}); err != nil {
		return Channel{}, fmt.Errorf("seed: clear channel plugin config %q: %w", plugin.Name, err)
	}
	return migrated, nil
}

// --- Helpers ---

func marshalSandboxConfig(cfg SandboxConfig) (string, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal sandbox config: %w", err)
	}
	return string(data), nil
}

func parseSandboxConfig(raw string) (SandboxConfig, error) {
	var cfg SandboxConfig
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return SandboxConfig{}, fmt.Errorf("parse sandbox config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return SandboxConfig{}, err
	}
	return cfg, nil
}

func (s *DBStore) seedProviders(ctx context.Context) error {
	providers, err := s.q.ListProviders(ctx)
	if err != nil {
		return fmt.Errorf("seed: list providers: %w", err)
	}
	if len(providers) > 0 {
		return nil
	}

	providerPlugins, err := s.q.ListPluginsByKind(ctx, PluginKindProvider)
	if err != nil {
		return fmt.Errorf("seed: list legacy provider plugins: %w", err)
	}
	if len(providerPlugins) > 0 {
		for _, row := range providerPlugins {
			plugin := pluginFromDB(row)
			legacy := legacyProviderFromPlugin(plugin)
			configJSON, err := json.Marshal(providerConfig(legacy))
			if err != nil {
				return fmt.Errorf("seed: marshal migrated provider %q: %w", legacy.ID, err)
			}
			if err := s.q.SeedProvider(ctx, sqlc.SeedProviderParams{
				ID:      legacy.ID,
				Type:    providerType(legacy),
				Name:    providerName(legacy),
				Enabled: boolToInt64(legacy.Enabled),
				Config:  string(configJSON),
			}); err != nil {
				return fmt.Errorf("seed: migrate provider %q: %w", legacy.ID, err)
			}
		}
		return nil
	}

	for _, name := range builtinProviderNames {
		provider := Provider{
			ID:      name,
			Type:    name,
			Name:    name,
			Enabled: true,
			APIKey:  "",
			BaseURL: "",
		}
		configJSON, err := json.Marshal(providerConfig(provider))
		if err != nil {
			return fmt.Errorf("seed: marshal provider config %q: %w", name, err)
		}
		if err := s.q.SeedProvider(ctx, sqlc.SeedProviderParams{
			ID:      provider.ID,
			Type:    provider.Type,
			Name:    provider.Name,
			Enabled: 1,
			Config:  string(configJSON),
		}); err != nil {
			return fmt.Errorf("seed: provider %q: %w", name, err)
		}
	}
	return nil
}

func providerFromDB(r sqlc.SettingsProvider) Provider {
	cfg := map[string]any{}
	if r.Config != "" {
		_ = json.Unmarshal([]byte(r.Config), &cfg)
	}
	apiKey, _ := cfg["api_key"].(string)
	baseURL, _ := cfg["base_url"].(string)
	return Provider{
		ID:      r.ID,
		Type:    r.Type,
		Name:    providerDisplayName(r.Name, r.ID),
		Enabled: r.Enabled == 1,
		APIKey:  apiKey,
		BaseURL: baseURL,
		Models:  providerModelsFromAny(cfg["models"]),
	}
}

func legacyProviderFromPlugin(p Plugin) Provider {
	apiKey, _ := p.Config["api_key"].(string)
	baseURL, _ := p.Config["base_url"].(string)
	typeName := p.Name
	if value, ok := p.Config["type"].(string); ok && value != "" {
		typeName = value
	}
	displayName, _ := p.Config["display_name"].(string)
	return Provider{
		ID:      p.Name,
		Type:    typeName,
		Name:    providerDisplayName(displayName, p.Name),
		Enabled: p.Enabled,
		APIKey:  apiKey,
		BaseURL: baseURL,
		Models:  providerModelsFromAny(p.Config["models"]),
	}
}

type providerConfigPayload struct {
	APIKey  string                   `json:"api_key"`
	BaseURL string                   `json:"base_url"`
	Models  map[string]ProviderModel `json:"models,omitempty"`
}

func providerConfig(p Provider) providerConfigPayload {
	return providerConfigPayload{
		APIKey:  p.APIKey,
		BaseURL: p.BaseURL,
		Models:  normalizeProviderModels(p.Models),
	}
}

func providerType(p Provider) string {
	if p.Type != "" {
		return p.Type
	}
	return p.ID
}

func providerName(p Provider) string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}

func providerDisplayName(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func normalizeProviderModels(models map[string]ProviderModel) map[string]ProviderModel {
	if len(models) == 0 {
		return nil
	}
	out := make(map[string]ProviderModel, len(models))
	for id, model := range models {
		if model.ID == "" {
			model.ID = id
		}
		if model.Name == "" {
			model.Name = id
		}
		if !model.Enabled {
			model.Enabled = false
		}
		out[id] = model
	}
	return out
}

func providerModelsFromAny(value any) map[string]ProviderModel {
	if value == nil {
		return nil
	}
	rawModels, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	models := make(map[string]ProviderModel, len(rawModels))
	for id, raw := range rawModels {
		data, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var model ProviderModel
		if err := json.Unmarshal(data, &model); err != nil {
			continue
		}
		rawModel, _ := raw.(map[string]any)
		if _, hasEnabled := rawModel["enabled"]; !hasEnabled {
			model.Enabled = true
		}
		if model.ID == "" {
			model.ID = id
		}
		if model.Name == "" {
			model.Name = id
		}
		models[id] = model
	}
	return models
}

// providerEnvVars maps provider slugs to their (apiKey, baseURL) env var names.
var providerEnvVars = map[string][2]string{
	"anthropic":       {"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"},
	"openai":          {"OPENAI_API_KEY", "OPENAI_BASE_URL"},
	"openai-response": {"OPENAI_API_KEY", "OPENAI_BASE_URL"},
}

// applyProviderEnvFallback fills empty API key and base URL from environment
// variables for known provider slugs.
func applyProviderEnvFallback(p *Provider) {
	providerKey := p.Type
	if providerKey == "" {
		providerKey = p.ID
	}
	if envs, ok := providerEnvVars[providerKey]; ok {
		envFallback(&p.APIKey, envs[0])
		envFallback(&p.BaseURL, envs[1])
	}
}

func envFallback(dst *string, envKey string) {
	if *dst == "" {
		if v := os.Getenv(envKey); v != "" {
			*dst = v
		}
	}
}

func agentFromDB(r sqlc.SettingsAgent) (Agent, error) {
	scope := r.Scope
	if scope == "" {
		scope = AgentScopeSystem
	}
	sandboxCfg, err := parseSandboxConfig(r.Sandbox)
	if err != nil {
		return Agent{}, fmt.Errorf("parse agent %q sandbox config: %w", r.ID, err)
	}
	return Agent{
		ID:           r.ID,
		Name:         r.Name,
		Model:        r.Model,
		ModelStrong:  r.ModelStrong,
		ModelFast:    r.ModelFast,
		SystemPrompt: r.SystemPrompt,
		Workspace:    r.Workspace,
		Sandbox:      sandboxCfg,
		Scope:        scope,
		CreatorID:    r.CreatorID,
		Enabled:      r.Enabled == 1,
	}, nil
}

// collectProviderIDs extracts unique provider IDs from model refs.
func collectProviderIDs(models ...string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range models {
		if m == "" {
			continue
		}
		pid, _ := ParseModelRef(m)
		if pid != "" && !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out
}

func pluginFromDB(r sqlc.SettingsPlugin) Plugin {
	var cfg map[string]any
	if r.Config != "" && r.Config != "{}" {
		_ = json.Unmarshal([]byte(r.Config), &cfg)
	}
	if cfg == nil {
		cfg = make(map[string]any)
	}
	return Plugin{
		ID:      r.ID,
		Kind:    r.Kind,
		Name:    r.Name,
		Enabled: r.Enabled == 1,
		Config:  cfg,
	}
}

func channelFromDB(r sqlc.SettingsChannel) Channel {
	agentID := ""
	if r.AgentID.Valid {
		agentID = r.AgentID.String
	}
	channelType := r.Type
	if channelType == "" {
		channelType = r.ID
	}
	return Channel{
		ID:      r.ID,
		Type:    channelType,
		AgentID: agentID,
		Enabled: r.Enabled == 1,
		Config:  r.Config,
	}
}
