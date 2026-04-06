package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vaayne/anna/internal/db/sqlc"
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

// --- Providers (backed by settings_plugins with kind=provider) ---

func (s *DBStore) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.q.ListPluginsByKind(ctx, PluginKindProvider)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	out := make([]Provider, len(rows))
	for i, r := range rows {
		out[i] = providerFromPlugin(pluginFromDB(r))
	}
	return out, nil
}

func (s *DBStore) GetProvider(ctx context.Context, id string) (Provider, error) {
	pluginID := PluginID(PluginKindProvider, id)
	r, err := s.q.GetPlugin(ctx, pluginID)
	if err != nil {
		return Provider{}, fmt.Errorf("get provider %q: %w", id, err)
	}
	p := providerFromPlugin(pluginFromDB(r))
	applyProviderEnvFallback(&p)
	return p, nil
}

func (s *DBStore) CreateProvider(ctx context.Context, p Provider) error {
	plug := pluginFromProvider(p)
	return s.UpsertPlugin(ctx, plug)
}

func (s *DBStore) UpdateProvider(ctx context.Context, p Provider) error {
	plug := pluginFromProvider(p)
	if err := s.SetPluginConfig(ctx, plug.ID, plug.Config); err != nil {
		return fmt.Errorf("update provider %q: %w", p.ID, err)
	}
	return nil
}

func (s *DBStore) DeleteProvider(ctx context.Context, id string) error {
	return s.q.DeletePlugin(ctx, PluginID(PluginKindProvider, id))
}

// --- Agents ---

func (s *DBStore) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.q.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	out := make([]Agent, len(rows))
	for i, r := range rows {
		out[i] = agentFromDB(r)
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
		out[i] = agentFromDB(r)
	}
	return out, nil
}

func (s *DBStore) GetAgent(ctx context.Context, id string) (Agent, error) {
	r, err := s.q.GetAgent(ctx, id)
	if err != nil {
		return Agent{}, fmt.Errorf("get agent %q: %w", id, err)
	}
	return agentFromDB(r), nil
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
	_, err := s.q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID:           a.ID,
		Name:         a.Name,
		Model:        a.Model,
		ModelStrong:  a.ModelStrong,
		ModelFast:    a.ModelFast,
		SystemPrompt: a.SystemPrompt,
		Workspace:    a.Workspace,
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
	err := s.q.UpdateAgent(ctx, sqlc.UpdateAgentParams{
		ID:           a.ID,
		Name:         a.Name,
		Model:        a.Model,
		ModelStrong:  a.ModelStrong,
		ModelFast:    a.ModelFast,
		SystemPrompt: a.SystemPrompt,
		Workspace:    a.Workspace,
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

func (s *DBStore) GetChannel(ctx context.Context, id string) (Channel, error) {
	r, err := s.q.GetChannel(ctx, id)
	if err != nil {
		return Channel{}, fmt.Errorf("get channel %q: %w", id, err)
	}
	return channelFromDB(r), nil
}

func (s *DBStore) UpsertChannel(ctx context.Context, ch Channel) error {
	enabled := int64(0)
	if ch.Enabled {
		enabled = 1
	}
	return s.q.UpsertChannel(ctx, sqlc.UpsertChannelParams{
		ID:      ch.ID,
		Enabled: enabled,
		Config:  ch.Config,
	})
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

func (s *DBStore) GetChatAgent(ctx context.Context, platform, chatID string) (string, error) {
	r, err := s.q.GetChatAgent(ctx, sqlc.GetChatAgentParams{
		Platform: platform,
		ChatID:   chatID,
	})
	if err != nil {
		return "", fmt.Errorf("get chat agent: %w", err)
	}
	return r.AgentID, nil
}

func (s *DBStore) SetChatAgent(ctx context.Context, platform, chatID, agentID string) error {
	return s.q.UpsertChatAgent(ctx, sqlc.UpsertChatAgentParams{
		Platform: platform,
		ChatID:   chatID,
		AgentID:  agentID,
	})
}

func (s *DBStore) DeleteChatAgent(ctx context.Context, platform, chatID string) error {
	return s.q.DeleteChatAgent(ctx, sqlc.DeleteChatAgentParams{
		Platform: platform,
		ChatID:   chatID,
	})
}

// --- User Agent Memory ---

func (s *DBStore) GetUserAgentMemory(ctx context.Context, userID int64, agentID string) (string, error) {
	r, err := s.q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("get user agent memory: %w", err)
	}
	return r.Content, nil
}

func (s *DBStore) SetUserAgentMemory(ctx context.Context, userID int64, agentID, content string) error {
	return s.q.UpsertUserAgentMemory(ctx, sqlc.UpsertUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
		Content: content,
	})
}

func (s *DBStore) ListUserMemories(ctx context.Context, userID int64) ([]UserAgentMemory, error) {
	rows, err := s.q.ListUserAgentMemoriesByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list user memories for user %d: %w", userID, err)
	}
	out := make([]UserAgentMemory, len(rows))
	for i, r := range rows {
		out[i] = UserAgentMemory{
			UserID:    r.UserID,
			AgentID:   r.AgentID,
			Content:   r.Content,
			UpdatedAt: r.UpdatedAt,
		}
	}
	return out, nil
}

func (s *DBStore) DeleteUserAgentMemory(ctx context.Context, userID int64, agentID string) error {
	return s.q.DeleteUserAgentMemory(ctx, sqlc.DeleteUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
}

// --- Settings ---

func (s *DBStore) GetSetting(ctx context.Context, key string) (string, error) {
	r, err := s.q.GetSetting(ctx, key)
	if err != nil {
		if err == sql.ErrNoRows {
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

	// Load all plugins in one query — providers are extracted from this list.
	pluginRows, err := s.q.ListPlugins(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot: list plugins: %w", err)
	}
	plugins := make([]Plugin, len(pluginRows))
	for i, r := range pluginRows {
		plugins[i] = pluginFromDB(r)
	}

	// Collect unique provider IDs from all model refs.
	provIDs := collectProviderIDs(ag.Model, ag.ModelStrong, ag.ModelFast)

	// Extract provider credentials from the loaded plugins.
	providers := make(map[string]ProviderCreds, len(provIDs))
	for _, pid := range provIDs {
		for i := range plugins {
			if plugins[i].Kind == PluginKindProvider && plugins[i].Name == pid {
				p := providerFromPlugin(plugins[i])
				applyProviderEnvFallback(&p)
				providers[pid] = ProviderCreds{APIKey: p.APIKey, BaseURL: p.BaseURL}
				break
			}
		}
	}

	defaultProvID, _ := ParseModelRef(ag.Model)
	defaultCreds := providers[defaultProvID]

	snap := &Snapshot{
		AgentID:      agentID,
		Provider:     defaultProvID,
		Model:        ag.Model,
		ModelStrong:  ag.ModelStrong,
		ModelFast:    ag.ModelFast,
		Workspace:    ag.Workspace,
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
	if val, err := s.GetSetting(ctx, "self_improve"); err == nil && val != "" {
		_ = json.Unmarshal([]byte(val), &snap.SelfImprove)
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
		_, err := s.q.CreateAgent(ctx, sqlc.CreateAgentParams{
			ID:           "anna",
			Name:         "Anna",
			Model:        "anthropic/claude-sonnet-4-6",
			SystemPrompt: defaultAnnaSoul,
			Workspace:    workspace,
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

	return nil
}

// seedPlugins migrates settings_channels rows into settings_plugins and
// seeds all 5 built-in plugin entries (1 tool + 4 channels). It is
// idempotent: existing rows are preserved via INSERT OR IGNORE.
func (s *DBStore) seedPlugins(ctx context.Context) error {
	// Check if any plugins already exist (skip channel migration if so).
	existing, err := s.q.ListPlugins(ctx)
	if err != nil {
		return fmt.Errorf("seed: list plugins: %w", err)
	}

	if len(existing) == 0 {
		// Migrate settings_channels → settings_plugins.
		channels, err := s.q.ListChannels(ctx)
		if err != nil {
			return fmt.Errorf("seed: list channels for migration: %w", err)
		}
		for _, ch := range channels {
			err := s.q.SeedPlugin(ctx, sqlc.SeedPluginParams{
				ID:      PluginID(PluginKindChannel, ch.ID),
				Kind:    PluginKindChannel,
				Name:    ch.ID,
				Enabled: ch.Enabled,
				Config:  ch.Config,
			})
			if err != nil {
				return fmt.Errorf("seed: migrate channel %q: %w", ch.ID, err)
			}
		}
	}

	// Seed all built-in plugins with INSERT OR IGNORE to preserve
	// user-modified state.
	for _, name := range builtinToolNames {
		err := s.q.SeedPlugin(ctx, sqlc.SeedPluginParams{
			ID:      PluginID(PluginKindTool, name),
			Kind:    PluginKindTool,
			Name:    name,
			Enabled: 1,
			Config:  "{}",
		})
		if err != nil {
			return fmt.Errorf("seed: plugin %s/%s: %w", PluginKindTool, name, err)
		}
	}
	for _, name := range builtinChannelNames {
		err := s.q.SeedPlugin(ctx, sqlc.SeedPluginParams{
			ID:      PluginID(PluginKindChannel, name),
			Kind:    PluginKindChannel,
			Name:    name,
			Enabled: 1,
			Config:  "{}",
		})
		if err != nil {
			return fmt.Errorf("seed: plugin %s/%s: %w", PluginKindChannel, name, err)
		}
	}
	for _, name := range builtinHookNames {
		err := s.q.SeedPlugin(ctx, sqlc.SeedPluginParams{
			ID:      PluginID(PluginKindHook, name),
			Kind:    PluginKindHook,
			Name:    name,
			Enabled: 1,
			Config:  "{}",
		})
		if err != nil {
			return fmt.Errorf("seed: plugin %s/%s: %w", PluginKindHook, name, err)
		}
	}
	for _, name := range builtinMemoryNames {
		enabled := int64(1)
		if name == "simple" {
			enabled = 0 // simple is opt-in; lcm is the default
		}
		err := s.q.SeedPlugin(ctx, sqlc.SeedPluginParams{
			ID:      PluginID(PluginKindMemory, name),
			Kind:    PluginKindMemory,
			Name:    name,
			Enabled: enabled,
			Config:  "{}",
		})
		if err != nil {
			return fmt.Errorf("seed: plugin %s/%s: %w", PluginKindMemory, name, err)
		}
	}

	// Only seed provider plugins on first bootstrap. If the user later
	// deletes a provider, it stays deleted across restarts.
	providerPlugins, err := s.q.ListPluginsByKind(ctx, PluginKindProvider)
	if err != nil {
		return fmt.Errorf("seed: list provider plugins: %w", err)
	}
	if len(providerPlugins) == 0 {
		for _, name := range builtinProviderNames {
			cfg := providerSeedConfig(name)
			cfgJSON, err := json.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("seed: marshal provider config %q: %w", name, err)
			}
			if err = s.q.SeedPlugin(ctx, sqlc.SeedPluginParams{
				ID:      PluginID(PluginKindProvider, name),
				Kind:    PluginKindProvider,
				Name:    name,
				Enabled: 1,
				Config:  string(cfgJSON),
			}); err != nil {
				return fmt.Errorf("seed: plugin %s/%s: %w", PluginKindProvider, name, err)
			}
		}
	}

	return nil
}

// --- Helpers ---

// providerFromPlugin extracts a typed Provider from a Plugin (kind=provider).
// api_key, base_url, and display_name are stored in the plugin's config JSON.
func providerFromPlugin(p Plugin) Provider {
	apiKey, _ := p.Config["api_key"].(string)
	baseURL, _ := p.Config["base_url"].(string)
	displayName, _ := p.Config["display_name"].(string)
	if displayName == "" {
		displayName = p.Name
	}
	return Provider{
		ID:      p.Name, // plugin Name is the provider slug (e.g. "anthropic")
		Name:    displayName,
		APIKey:  apiKey,
		BaseURL: baseURL,
	}
}

// pluginFromProvider converts a Provider into a Plugin for storage.
func pluginFromProvider(p Provider) Plugin {
	name := p.ID
	return Plugin{
		ID:      PluginID(PluginKindProvider, name),
		Kind:    PluginKindProvider,
		Name:    name,
		Enabled: true,
		Config:  map[string]any{"api_key": p.APIKey, "base_url": p.BaseURL, "display_name": p.Name},
	}
}

// providerEnvVars maps provider slugs to their (apiKey, baseURL) env var names.
var providerEnvVars = map[string][2]string{
	"anthropic":       {"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"},
	"openai":          {"OPENAI_API_KEY", "OPENAI_BASE_URL"},
	"openai-response": {"OPENAI_API_KEY", "OPENAI_BASE_URL"},
}

// providerSeedConfig returns the initial config map for a provider plugin,
// populated from environment variables when available.
func providerSeedConfig(name string) map[string]any {
	cfg := map[string]any{"api_key": "", "base_url": "", "display_name": name}
	if envs, ok := providerEnvVars[name]; ok {
		cfg["api_key"] = os.Getenv(envs[0])
		cfg["base_url"] = os.Getenv(envs[1])
	}
	return cfg
}

// applyProviderEnvFallback fills empty API key and base URL from environment
// variables for known provider slugs.
func applyProviderEnvFallback(p *Provider) {
	if envs, ok := providerEnvVars[p.ID]; ok {
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

func agentFromDB(r sqlc.SettingsAgent) Agent {
	scope := r.Scope
	if scope == "" {
		scope = AgentScopeSystem
	}
	return Agent{
		ID:           r.ID,
		Name:         r.Name,
		Model:        r.Model,
		ModelStrong:  r.ModelStrong,
		ModelFast:    r.ModelFast,
		SystemPrompt: r.SystemPrompt,
		Workspace:    r.Workspace,
		Scope:        scope,
		CreatorID:    r.CreatorID,
		Enabled:      r.Enabled == 1,
	}
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
	return Channel{
		ID:      r.ID,
		Enabled: r.Enabled == 1,
		Config:  r.Config,
	}
}
