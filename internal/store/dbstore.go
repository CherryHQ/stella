package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/orgctx"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// DBStore implements config.Store using sqlc queries backed by SQLite.
type DBStore struct {
	q *sqlc.Queries
}

// NewDBStore creates a new DBStore wrapping the given database connection.
// It sets MaxOpenConns(1) to mitigate SQLite concurrency issues.
func NewDBStore(db *sql.DB) *DBStore {
	db.SetMaxOpenConns(1)
	return &DBStore{q: sqlc.New(db)}
}

func requireOrgID(ctx context.Context) (string, error) {
	orgID := orgctx.OrgIDFromContext(ctx)
	if orgID == "" {
		return "", fmt.Errorf("org_id is required in context")
	}
	return orgID, nil
}

// --- Providers (backed by settings_provider) ---

func (s *DBStore) ListProviders(ctx context.Context) ([]config.Provider, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListProviders(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	out := make([]config.Provider, len(rows))
	for i, r := range rows {
		out[i] = providerFromDB(r)
		applyProviderEnvFallback(&out[i])
	}
	return out, nil
}

func (s *DBStore) GetProvider(ctx context.Context, id string) (config.Provider, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return config.Provider{}, err
	}
	r, err := s.q.GetProvider(ctx, sqlc.GetProviderParams{ID: id, OrgID: orgID})
	if err != nil {
		return config.Provider{}, fmt.Errorf("get provider %q: %w", id, err)
	}
	p := providerFromDB(r)
	applyProviderEnvFallback(&p)
	return p, nil
}

func (s *DBStore) CreateProvider(ctx context.Context, p config.Provider) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
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
		OrgID:   orgID,
	}); err != nil {
		return fmt.Errorf("create provider %q: %w", p.ID, err)
	}
	return nil
}

func (s *DBStore) UpdateProvider(ctx context.Context, p config.Provider) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
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
		OrgID:   orgID,
	}); err != nil {
		return fmt.Errorf("update provider %q: %w", p.ID, err)
	}
	return nil
}

func (s *DBStore) SetProviderOrg(ctx context.Context, providerID, orgID string) error {
	return s.q.SetProviderOrg(ctx, sqlc.SetProviderOrgParams{
		OrgID: orgID,
		ID:    providerID,
	})
}

func (s *DBStore) DeleteProvider(ctx context.Context, id string) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	return s.q.DeleteProvider(ctx, sqlc.DeleteProviderParams{ID: id, OrgID: orgID})
}

// --- Agents ---

func (s *DBStore) ListAgents(ctx context.Context) ([]config.Agent, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAgents(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	out := make([]config.Agent, len(rows))
	for i, r := range rows {
		agent, err := agentFromDB(r)
		if err != nil {
			return nil, fmt.Errorf("list agents: %w", err)
		}
		out[i] = agent
	}
	return out, nil
}

func (s *DBStore) ListEnabledAgents(ctx context.Context) ([]config.Agent, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListEnabledAgents(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list enabled agents: %w", err)
	}
	out := make([]config.Agent, len(rows))
	for i, r := range rows {
		agent, err := agentFromDB(r)
		if err != nil {
			return nil, fmt.Errorf("list enabled agents: %w", err)
		}
		out[i] = agent
	}
	return out, nil
}

func (s *DBStore) ListAccessibleAgents(ctx context.Context, userID string) ([]config.Agent, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAccessibleAgents(ctx, sqlc.ListAccessibleAgentsParams{
		OrgID:  orgID,
		UserID: userID,
	})
	if err != nil {
		return nil, fmt.Errorf("list accessible agents: %w", err)
	}
	out := make([]config.Agent, len(rows))
	for i, r := range rows {
		agent, err := agentFromDB(r)
		if err != nil {
			return nil, fmt.Errorf("list accessible agents: %w", err)
		}
		out[i] = agent
	}
	return out, nil
}

func (s *DBStore) GetAgent(ctx context.Context, id string) (config.Agent, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return config.Agent{}, err
	}
	r, err := s.q.GetAgent(ctx, sqlc.GetAgentParams{ID: id, OrgID: orgID})
	if err != nil {
		return config.Agent{}, fmt.Errorf("get agent %q: %w", id, err)
	}
	agent, err := agentFromDB(r)
	if err != nil {
		return config.Agent{}, fmt.Errorf("get agent %q: %w", id, err)
	}
	return agent, nil
}

func (s *DBStore) CreateAgent(ctx context.Context, a config.Agent) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	enabled := int64(0)
	if a.Enabled {
		enabled = 1
	}
	scope := a.Scope
	if scope == "" {
		scope = config.AgentScopeSystem
	}
	if err := a.Sandbox.Validate(); err != nil {
		return fmt.Errorf("create agent %q: %w", a.ID, err)
	}
	sandboxJSON, err := marshalSandboxConfig(a.Sandbox)
	if err != nil {
		return fmt.Errorf("create agent %q: %w", a.ID, err)
	}
	_, err = s.q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID:                   a.ID,
		Name:                 a.Name,
		Model:                a.Model,
		ModelStrong:          a.ModelStrong,
		ModelFast:            a.ModelFast,
		SystemPrompt:         a.SystemPrompt,
		Soul:                 a.Soul,
		Workspace:            a.Workspace,
		Sandbox:              sandboxJSON,
		EnabledBuiltinSkills: "[]",
		Scope:                scope,
		CreatorID:            a.CreatorID,
		Enabled:              enabled,
		OrgID:                orgID,
	})
	if err != nil {
		return fmt.Errorf("create agent %q: %w", a.ID, err)
	}
	return nil
}

func (s *DBStore) UpdateAgent(ctx context.Context, a config.Agent) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	enabled := int64(0)
	if a.Enabled {
		enabled = 1
	}
	scope := a.Scope
	if scope == "" {
		scope = config.AgentScopeSystem
	}
	if err := a.Sandbox.Validate(); err != nil {
		return fmt.Errorf("update agent %q: %w", a.ID, err)
	}
	sandboxJSON, err := marshalSandboxConfig(a.Sandbox)
	if err != nil {
		return fmt.Errorf("update agent %q: %w", a.ID, err)
	}
	err = s.q.UpdateAgent(ctx, sqlc.UpdateAgentParams{
		ID:                   a.ID,
		OrgID:                orgID,
		Name:                 a.Name,
		Model:                a.Model,
		ModelStrong:          a.ModelStrong,
		ModelFast:            a.ModelFast,
		SystemPrompt:         a.SystemPrompt,
		Soul:                 a.Soul,
		Workspace:            a.Workspace,
		Sandbox:              sandboxJSON,
		EnabledBuiltinSkills: "[]",
		Scope:                scope,
		Enabled:              enabled,
	})
	if err != nil {
		return fmt.Errorf("update agent %q: %w", a.ID, err)
	}
	return nil
}

func (s *DBStore) SetAgentOrg(ctx context.Context, agentID, orgID string) error {
	return s.q.SetAgentOrg(ctx, sqlc.SetAgentOrgParams{
		OrgID: orgID,
		ID:    agentID,
	})
}

func (s *DBStore) DeleteAgent(ctx context.Context, id string) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	return s.q.DeleteAgent(ctx, sqlc.DeleteAgentParams{ID: id, OrgID: orgID})
}

// --- Channels ---

func (s *DBStore) ListChannels(ctx context.Context) ([]config.Channel, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListChannels(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	out := make([]config.Channel, len(rows))
	for i, r := range rows {
		out[i] = channelFromDB(r)
	}
	return out, nil
}

func (s *DBStore) ListChannelsByType(ctx context.Context, channelType string) ([]config.Channel, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListChannelsByType(ctx, sqlc.ListChannelsByTypeParams{
		Type:  channelType,
		OrgID: orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("list %s channels: %w", channelType, err)
	}
	out := make([]config.Channel, len(rows))
	for i, r := range rows {
		out[i] = channelFromDB(r)
	}
	return out, nil
}

func (s *DBStore) GetChannel(ctx context.Context, id string) (config.Channel, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return config.Channel{}, err
	}
	r, err := s.q.GetChannel(ctx, sqlc.GetChannelParams{ID: id, OrgID: orgID})
	if err != nil {
		return config.Channel{}, fmt.Errorf("get channel %q: %w", id, err)
	}
	return channelFromDB(r), nil
}

func (s *DBStore) UpsertChannel(ctx context.Context, ch config.Channel) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	if ch.ID == "" {
		ch.ID = uuid.NewString()
	}
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
		OrgID:   orgID,
	})
}

func (s *DBStore) SetChannelOrg(ctx context.Context, channelID, orgID string) error {
	currentOrgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	return s.q.SetChannelOrg(ctx, sqlc.SetChannelOrgParams{
		OrgID:        orgID,
		ID:           channelID,
		CurrentOrgID: currentOrgID,
	})
}

func (s *DBStore) DeleteChannel(ctx context.Context, id string) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	return s.q.DeleteChannel(ctx, sqlc.DeleteChannelParams{ID: id, OrgID: orgID})
}

// --- Plugins ---

func (s *DBStore) ListPlugins(ctx context.Context) ([]config.Plugin, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListPlugins(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list plugins: %w", err)
	}
	out := make([]config.Plugin, len(rows))
	for i, r := range rows {
		out[i] = pluginFromDB(r)
	}
	return out, nil
}

func (s *DBStore) ListPluginsByKind(ctx context.Context, kind string) ([]config.Plugin, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListPluginsByKind(ctx, sqlc.ListPluginsByKindParams{
		Kind:  kind,
		OrgID: orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("list plugins by kind %q: %w", kind, err)
	}
	out := make([]config.Plugin, len(rows))
	for i, r := range rows {
		out[i] = pluginFromDB(r)
	}
	return out, nil
}

func (s *DBStore) ListEnabledPlugins(ctx context.Context) ([]config.Plugin, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListEnabledPlugins(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list enabled plugins: %w", err)
	}
	out := make([]config.Plugin, len(rows))
	for i, r := range rows {
		out[i] = pluginFromDB(r)
	}
	return out, nil
}

func (s *DBStore) GetPlugin(ctx context.Context, id string) (config.Plugin, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return config.Plugin{}, err
	}
	r, err := s.q.GetPlugin(ctx, sqlc.GetPluginParams{ID: id, OrgID: orgID})
	if err != nil {
		return config.Plugin{}, fmt.Errorf("get plugin %q: %w", id, err)
	}
	return pluginFromDB(r), nil
}

func (s *DBStore) UpsertPlugin(ctx context.Context, p config.Plugin) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
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
		OrgID:   orgID,
	})
}

func (s *DBStore) SetPluginEnabled(ctx context.Context, id string, enabled bool) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	v := int64(0)
	if enabled {
		v = 1
	}
	return s.q.UpdatePluginEnabled(ctx, sqlc.UpdatePluginEnabledParams{
		Enabled: v,
		ID:      id,
		OrgID:   orgID,
	})
}

func (s *DBStore) SetPluginConfig(ctx context.Context, id string, cfg map[string]any) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal plugin config %q: %w", id, err)
	}
	return s.q.UpdatePluginConfig(ctx, sqlc.UpdatePluginConfigParams{
		Config: string(configJSON),
		ID:     id,
		OrgID:  orgID,
	})
}

func (s *DBStore) DeletePlugin(ctx context.Context, id string) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	return s.q.DeletePlugin(ctx, sqlc.DeletePluginParams{ID: id, OrgID: orgID})
}

// --- Chat Agents ---

func (s *DBStore) GetChatAgent(ctx context.Context, channelID, platform, chatID string) (string, error) {
	if channelID == "" {
		channelID = platform
	}
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return "", err
	}
	r, err := s.q.GetChatAgent(ctx, sqlc.GetChatAgentParams{
		ChannelID: channelID,
		Platform:  platform,
		ChatID:    chatID,
		OrgID:     orgID,
	})
	if err != nil {
		r, err = s.q.GetLegacyChatAgent(ctx, sqlc.GetLegacyChatAgentParams{
			Platform: platform,
			ChatID:   chatID,
			OrgID:    orgID,
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
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	return s.q.UpsertChatAgent(ctx, sqlc.UpsertChatAgentParams{
		ChannelID: channelID,
		Platform:  platform,
		ChatID:    chatID,
		AgentID:   agentID,
		OrgID:     orgID,
	})
}

func (s *DBStore) DeleteChatAgent(ctx context.Context, channelID, platform, chatID string) error {
	if channelID == "" {
		channelID = platform
	}
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	return s.q.DeleteChatAgent(ctx, sqlc.DeleteChatAgentParams{
		ChannelID: channelID,
		Platform:  platform,
		ChatID:    chatID,
		OrgID:     orgID,
	})
}

// --- Settings ---

func (s *DBStore) GetSetting(ctx context.Context, key string) (string, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return "", err
	}
	r, err := s.q.GetSetting(ctx, sqlc.GetSettingParams{Key: key, OrgID: orgID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("get setting %q: %w", key, err)
	}
	return r.Value, nil
}

func (s *DBStore) SetSetting(ctx context.Context, key, value string) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	return s.q.UpsertSetting(ctx, sqlc.UpsertSettingParams{
		Key:   key,
		Value: value,
		OrgID: orgID,
	})
}

// --- Snapshot ---

func (s *DBStore) Snapshot(ctx context.Context, agentID string) (*config.Snapshot, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return nil, err
	}
	ag, err := s.q.GetAgent(ctx, sqlc.GetAgentParams{ID: agentID, OrgID: orgID})
	if err != nil {
		return nil, fmt.Errorf("snapshot: get agent %q: %w", agentID, err)
	}

	pluginRows, err := s.q.ListPlugins(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("snapshot: list plugins: %w", err)
	}
	plugins := make([]config.Plugin, len(pluginRows))
	for i, r := range pluginRows {
		plugins[i] = pluginFromDB(r)
	}

	providers, defaultCreds, err := s.resolveProviders(ctx, orgID, ag.Model, ag.ModelStrong, ag.ModelFast)
	if err != nil {
		return nil, err
	}

	defaultProvID, _ := config.ParseModelRef(ag.Model)

	sandboxCfg, err := parseSandboxConfig(ag.Sandbox)
	if err != nil {
		return nil, fmt.Errorf("snapshot: parse agent sandbox config %q: %w", agentID, err)
	}

	snap := &config.Snapshot{
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
		Soul:         ag.Soul,
		Providers:    providers,
		Plugins:      plugins,
	}

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
	if snap.Runner.Type == "" {
		snap.Runner.Type = "go"
	}
	if snap.Runner.IdleTimeout == 0 {
		snap.Runner.IdleTimeout = 10
	}

	return snap, nil
}

func (s *DBStore) resolveProviders(ctx context.Context, orgID string, models ...string) (map[string]config.ProviderCreds, config.ProviderCreds, error) {
	provIDs := collectProviderIDs(models...)
	rows, err := s.q.ListProviders(ctx, orgID)
	if err != nil {
		return nil, config.ProviderCreds{}, fmt.Errorf("snapshot: list providers: %w", err)
	}

	byID := make(map[string]config.Provider, len(rows))
	typeCount := make(map[string]int)
	for _, row := range rows {
		p := providerFromDB(row)
		applyProviderEnvFallback(&p)
		byID[p.ID] = p
		if p.Type != "" {
			typeCount[p.Type]++
		}
	}
	// Alias: if only one provider of a type exists, allow lookup by type name.
	for _, p := range byID {
		if p.Type != "" && typeCount[p.Type] == 1 {
			if _, exists := byID[p.Type]; !exists {
				byID[p.Type] = p
			}
		}
	}

	creds := make(map[string]config.ProviderCreds, len(provIDs))
	for _, pid := range provIDs {
		if p, ok := byID[pid]; ok {
			creds[pid] = config.ProviderCreds{Type: p.Type, APIKey: p.APIKey, BaseURL: p.BaseURL}
		}
	}

	var defaultModel string
	if len(models) > 0 {
		defaultModel = models[0]
	}
	defaultProvID, _ := config.ParseModelRef(defaultModel)
	defaultCreds := creds[defaultProvID]

	return creds, defaultCreds, nil
}

// --- Bootstrap ---

const defaultStellaSoul = `You are Stella — a sharp, efficient personal AI assistant.

- Warm but not chatty. Friendly but not performative.
- Lead with answers, not preamble.
- Match the user's energy: casual when they're casual, precise when they need precision.
- Own your mistakes quickly. No hedging or over-apologizing.
- Use humor sparingly and naturally — never forced.`

func (s *DBStore) SeedDefaults(ctx context.Context, orgID string) error {
	ctx = config.WithOrgID(ctx, orgID)
	if err := s.seedPlugins(ctx, orgID); err != nil {
		return err
	}
	if err := s.seedChannelInstances(ctx, orgID); err != nil {
		return err
	}
	if err := s.seedProviders(ctx, orgID); err != nil {
		return err
	}

	agents, err := s.q.ListAgents(ctx, orgID)
	if err != nil {
		return fmt.Errorf("seed: list agents: %w", err)
	}
	if len(agents) > 0 {
		return nil
	}
	workspace := filepath.Join(config.StellaHome(), "workspaces", "stella")
	sandboxJSON, err := marshalSandboxConfig(config.SandboxConfig{})
	if err != nil {
		return fmt.Errorf("seed: marshal stella sandbox config: %w", err)
	}
	providers, err := s.ListProviders(ctx)
	if err != nil {
		return fmt.Errorf("seed: list providers: %w", err)
	}
	_, err = s.q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID:                   uuid.NewString(),
		Name:                 "Stella",
		Model:                config.DefaultAgentModelRef(providers),
		SystemPrompt:         defaultStellaSoul,
		Workspace:            workspace,
		Sandbox:              sandboxJSON,
		EnabledBuiltinSkills: "[]",
		Scope:                config.AgentScopeSystem,
		Enabled:              1,
		OrgID:                orgID,
	})
	if err != nil {
		return fmt.Errorf("seed: create stella agent: %w", err)
	}

	return nil
}

func (s *DBStore) seedPlugins(ctx context.Context, orgID string) error {
	if err := s.seedBuiltinPlugins(ctx, orgID, config.PluginKindTool, config.BuiltinToolNames, func(name string) int64 {
		switch name {
		case "mcp", "webfetch":
			return 0
		default:
			return 1
		}
	}); err != nil {
		return err
	}
	if err := s.seedBuiltinPlugins(ctx, orgID, config.PluginKindChannel, config.BuiltinChannelNames, nil); err != nil {
		return err
	}
	if err := s.seedBuiltinPlugins(ctx, orgID, config.PluginKindHook, config.BuiltinHookNames, nil); err != nil {
		return err
	}
	if err := s.seedBuiltinPlugins(ctx, orgID, config.PluginKindMemory, config.BuiltinMemoryNames, func(name string) int64 {
		if name == "simple" {
			return 0
		}
		return 1
	}); err != nil {
		return err
	}
	if err := s.seedBuiltinPlugins(ctx, orgID, config.PluginKindSandbox, config.BuiltinSandboxNames, func(name string) int64 {
		if name == config.SandboxBackendLocal {
			return 1
		}
		return 0
	}); err != nil {
		return err
	}
	if err := s.q.SeedPlugin(ctx, sqlc.SeedPluginParams{
		ID:      "reflect",
		Kind:    "reflect",
		Name:    "reflect",
		Enabled: 0,
		Config:  `{}`,
		OrgID:   orgID,
	}); err != nil {
		return fmt.Errorf("seed: plugin reflect: %w", err)
	}

	return nil
}

func (s *DBStore) seedChannelInstances(ctx context.Context, orgID string) error {
	rows, err := s.q.ListChannels(ctx, orgID)
	if err != nil {
		return fmt.Errorf("seed: list channel instances: %w", err)
	}
	existingTypes := make(map[string]bool, len(rows))
	for _, row := range rows {
		t := row.Type
		if t == "" {
			t = row.ID
		}
		existingTypes[t] = true
	}
	for _, name := range config.BuiltinChannelNames {
		if existingTypes[name] {
			continue
		}
		if err := s.UpsertChannel(ctx, config.Channel{
			ID:      uuid.NewString(),
			Type:    name,
			Enabled: true,
			Config:  "{}",
			OrgID:   orgID,
		}); err != nil {
			return fmt.Errorf("seed: default channel instance %q: %w", name, err)
		}
	}
	return nil
}

func (s *DBStore) seedBuiltinPlugins(ctx context.Context, orgID string, kind string, names []string, enabledFor func(string) int64) error {
	for _, name := range names {
		enabled := int64(1)
		if enabledFor != nil {
			enabled = enabledFor(name)
		}
		if err := s.q.SeedPlugin(ctx, sqlc.SeedPluginParams{
			ID:      config.PluginID(kind, name),
			Kind:    kind,
			Name:    name,
			Enabled: enabled,
			Config:  "{}",
			OrgID:   orgID,
		}); err != nil {
			return fmt.Errorf("seed: plugin %s/%s: %w", kind, name, err)
		}
	}
	return nil
}

func (s *DBStore) seedProviders(ctx context.Context, orgID string) error {
	providers, err := s.q.ListProviders(ctx, orgID)
	if err != nil {
		return fmt.Errorf("seed: list providers: %w", err)
	}
	if len(providers) > 0 {
		return nil
	}

	for _, name := range config.BuiltinProviderNames {
		provider := config.Provider{
			ID:      uuid.NewString(),
			Type:    name,
			Name:    name,
			Enabled: true,
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
			OrgID:   orgID,
		}); err != nil {
			return fmt.Errorf("seed: provider %q: %w", name, err)
		}
	}
	return nil
}

// --- Helpers ---

func marshalSandboxConfig(cfg config.SandboxConfig) (string, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal sandbox config: %w", err)
	}
	return string(data), nil
}

func parseSandboxConfig(raw string) (config.SandboxConfig, error) {
	var cfg config.SandboxConfig
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return config.SandboxConfig{}, fmt.Errorf("parse sandbox config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return config.SandboxConfig{}, err
	}
	return cfg, nil
}

func providerFromDB(r sqlc.SettingsProvider) config.Provider {
	cfg := map[string]any{}
	if r.Config != "" {
		_ = json.Unmarshal([]byte(r.Config), &cfg)
	}
	apiKey, _ := cfg["api_key"].(string)
	baseURL, _ := cfg["base_url"].(string)
	return config.Provider{
		ID:      r.ID,
		Type:    r.Type,
		Name:    providerDisplayName(r.Name, r.ID),
		Enabled: r.Enabled == 1,
		APIKey:  apiKey,
		BaseURL: baseURL,
		Models:  providerModelsFromAny(cfg["models"]),
		OrgID:   r.OrgID,
	}
}

type providerConfigPayload struct {
	APIKey  string                          `json:"api_key"`
	BaseURL string                          `json:"base_url"`
	Models  map[string]config.ProviderModel `json:"models,omitempty"`
}

func providerConfig(p config.Provider) providerConfigPayload {
	return providerConfigPayload{
		APIKey:  p.APIKey,
		BaseURL: p.BaseURL,
		Models:  normalizeProviderModels(p.Models),
	}
}

func providerType(p config.Provider) string {
	if p.Type != "" {
		return p.Type
	}
	return p.ID
}

func providerName(p config.Provider) string {
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

func normalizeProviderModels(models map[string]config.ProviderModel) map[string]config.ProviderModel {
	if len(models) == 0 {
		return nil
	}
	out := make(map[string]config.ProviderModel, len(models))
	for id, model := range models {
		if model.ID == "" {
			model.ID = id
		}
		if model.Name == "" {
			model.Name = id
		}
		out[id] = model
	}
	return out
}

func providerModelsFromAny(value any) map[string]config.ProviderModel {
	if value == nil {
		return nil
	}
	rawModels, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	models := make(map[string]config.ProviderModel, len(rawModels))
	for id, raw := range rawModels {
		data, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var model config.ProviderModel
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

var providerEnvVars = map[string][2]string{
	"anthropic":       {"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"},
	"openai":          {"OPENAI_API_KEY", "OPENAI_BASE_URL"},
	"openai-response": {"OPENAI_API_KEY", "OPENAI_BASE_URL"},
}

func applyProviderEnvFallback(p *config.Provider) {
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

func agentFromDB(r sqlc.SettingsAgent) (config.Agent, error) {
	scope := r.Scope
	if scope == "" {
		scope = config.AgentScopeSystem
	}
	sandboxCfg, err := parseSandboxConfig(r.Sandbox)
	if err != nil {
		return config.Agent{}, fmt.Errorf("parse agent %q sandbox config: %w", r.ID, err)
	}
	return config.Agent{
		ID:           r.ID,
		Name:         r.Name,
		Model:        r.Model,
		ModelStrong:  r.ModelStrong,
		ModelFast:    r.ModelFast,
		SystemPrompt: r.SystemPrompt,
		Soul:         r.Soul,
		Workspace:    r.Workspace,
		Sandbox:      sandboxCfg,
		Scope:        scope,
		CreatorID:    r.CreatorID,
		Enabled:      r.Enabled == 1,
		OrgID:        r.OrgID,
	}, nil
}

func collectProviderIDs(models ...string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range models {
		if m == "" {
			continue
		}
		pid, _ := config.ParseModelRef(m)
		if pid != "" && !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out
}

func pluginFromDB(r sqlc.SettingsPlugin) config.Plugin {
	var cfg map[string]any
	if r.Config != "" && r.Config != "{}" {
		_ = json.Unmarshal([]byte(r.Config), &cfg)
	}
	if cfg == nil {
		cfg = make(map[string]any)
	}
	return config.Plugin{
		ID:      r.ID,
		Kind:    r.Kind,
		Name:    r.Name,
		Enabled: r.Enabled == 1,
		Config:  cfg,
		OrgID:   r.OrgID,
	}
}

func channelFromDB(r sqlc.SettingsChannel) config.Channel {
	agentID := ""
	if r.AgentID.Valid {
		agentID = r.AgentID.String
	}
	channelType := r.Type
	if channelType == "" {
		channelType = r.ID
	}
	return config.Channel{
		ID:      r.ID,
		Type:    channelType,
		AgentID: agentID,
		Enabled: r.Enabled == 1,
		Config:  r.Config,
		OrgID:   r.OrgID,
	}
}
