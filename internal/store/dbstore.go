package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// DBStore implements config.Store using sqlc queries backed by PostgreSQL.
type DBStore struct {
	q *sqlc.Queries
	// pool is retained (not just wrapped by q) so composite writes that must be
	// atomic — the Agent + its encrypted Provider credentials — can open one
	// transaction via Queries.WithTx.
	pool *pgxpool.Pool
}

// NewDBStore creates a new DBStore wrapping the given database connection.
func NewDBStore(db *pgxpool.Pool) *DBStore {
	return &DBStore{q: sqlc.New(db), pool: db}
}

// --- Providers (backed by provider) ---

func (s *DBStore) ListProviders(ctx context.Context) ([]config.Provider, error) {
	rows, err := s.q.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	out := make([]config.Provider, len(rows))
	for i, r := range rows {
		out[i] = providerFromDB(r)
	}
	return out, nil
}

// ListProviderIDs returns canonical Provider row IDs without loading Provider
// config, which contains the deployment-global API key.
func (s *DBStore) ListProviderIDs(ctx context.Context) ([]string, error) {
	ids, err := s.q.ListProviderIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list provider ids: %w", err)
	}
	return ids, nil
}

func (s *DBStore) GetProvider(ctx context.Context, id string) (config.Provider, error) {
	r, err := s.q.GetProvider(ctx, id)
	if err != nil {
		return config.Provider{}, fmt.Errorf("get provider %q: %w", id, err)
	}
	return providerFromDB(r), nil
}

func (s *DBStore) CreateProvider(ctx context.Context, p config.Provider) error {
	if p.ID == "" {
		p.ID = uuid.Must(uuid.NewV7()).String()
	}
	configJSON, err := json.Marshal(providerConfig(p))
	if err != nil {
		return fmt.Errorf("create provider %q: marshal config: %w", p.ID, err)
	}
	if _, err := s.q.CreateProvider(ctx, sqlc.CreateProviderParams{
		ID:      p.ID,
		Type:    providerType(p),
		Name:    providerName(p),
		Enabled: p.Enabled,
		Config:  configJSON,
	}); err != nil {
		return fmt.Errorf("create provider %q: %w", p.ID, err)
	}
	return nil
}

func (s *DBStore) UpdateProvider(ctx context.Context, p config.Provider) error {
	configJSON, err := json.Marshal(providerConfig(p))
	if err != nil {
		return fmt.Errorf("update provider %q: marshal config: %w", p.ID, err)
	}
	if err := s.q.UpdateProvider(ctx, sqlc.UpdateProviderParams{
		Type:    providerType(p),
		Name:    providerName(p),
		Enabled: p.Enabled,
		Config:  configJSON,
		ID:      p.ID,
	}); err != nil {
		return fmt.Errorf("update provider %q: %w", p.ID, err)
	}
	return nil
}

func (s *DBStore) DeleteProvider(ctx context.Context, id string) error {
	return s.q.DeleteProvider(ctx, id)
}

// --- Fetched-model cache (backed by provider_models_cache) ---

func (s *DBStore) ListCachedModels(ctx context.Context) ([]config.CachedModel, error) {
	rows, err := s.q.ListProviderModelsCache(ctx)
	if err != nil {
		return nil, fmt.Errorf("list cached models: %w", err)
	}
	var out []config.CachedModel
	for _, r := range rows {
		var modelIDs []string
		if err := json.Unmarshal(r.Models, &modelIDs); err != nil {
			return nil, fmt.Errorf("list cached models: decode %q: %w", r.ProviderID, err)
		}
		for _, id := range modelIDs {
			out = append(out, config.CachedModel{Provider: r.ProviderID, Model: id})
		}
	}
	return out, nil
}

func (s *DBStore) ReplaceCachedModels(ctx context.Context, providerID string, modelIDs []string) error {
	if modelIDs == nil {
		modelIDs = []string{}
	}
	data, err := json.Marshal(modelIDs)
	if err != nil {
		return fmt.Errorf("replace cached models %q: marshal: %w", providerID, err)
	}
	if err := s.q.UpsertProviderModelsCache(ctx, sqlc.UpsertProviderModelsCacheParams{
		ProviderID: providerID,
		Models:     data,
	}); err != nil {
		return fmt.Errorf("replace cached models %q: %w", providerID, err)
	}
	return nil
}

// --- Agents ---

func (s *DBStore) ListAgents(ctx context.Context) ([]config.Agent, error) {
	rows, err := s.q.ListAgents(ctx)
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
	rows, err := s.q.ListEnabledAgents(ctx)
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
	rows, err := s.q.ListAccessibleAgents(ctx, userID)
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
	r, err := s.q.GetAgent(ctx, id)
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
	params, err := createAgentParams(a)
	if err != nil {
		return err
	}
	if _, err := s.q.CreateAgent(ctx, params); err != nil {
		return fmt.Errorf("create agent %q: %w", params.ID, err)
	}
	return nil
}

// createAgentParams mints a missing ID, applies the default scope, and validates
// and marshals the sandbox config into insert params. It is shared by the plain
// CreateAgent and the atomic Agent+credentials create so both write identical
// rows.
func createAgentParams(a config.Agent) (sqlc.CreateAgentParams, error) {
	if a.ID == "" {
		a.ID = uuid.Must(uuid.NewV7()).String()
	}
	scope := a.Scope
	if scope == "" {
		scope = config.AgentScopeSystem
	}
	if err := a.Sandbox.Validate(); err != nil {
		return sqlc.CreateAgentParams{}, fmt.Errorf("create agent %q: %w", a.ID, err)
	}
	sandboxJSON, err := marshalSandboxConfig(a.Sandbox)
	if err != nil {
		return sqlc.CreateAgentParams{}, fmt.Errorf("create agent %q: %w", a.ID, err)
	}
	return sqlc.CreateAgentParams{
		ID:                   a.ID,
		Name:                 a.Name,
		Model:                a.Model,
		ModelThinking:        a.ModelThinking,
		ModelStrong:          a.ModelStrong,
		ModelStrongThinking:  a.ModelStrongThinking,
		ModelFast:            a.ModelFast,
		ModelFastThinking:    a.ModelFastThinking,
		SystemPrompt:         a.SystemPrompt,
		Soul:                 a.Soul,
		Workspace:            a.Workspace,
		Sandbox:              sandboxJSON,
		EnabledBuiltinSkills: json.RawMessage("[]"),
		Scope:                scope,
		CreatorID:            a.CreatorID,
		Enabled:              a.Enabled,
	}, nil
}

func (s *DBStore) UpdateAgent(ctx context.Context, a config.Agent) error {
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
		Name:                 a.Name,
		Model:                a.Model,
		ModelThinking:        a.ModelThinking,
		ModelStrong:          a.ModelStrong,
		ModelStrongThinking:  a.ModelStrongThinking,
		ModelFast:            a.ModelFast,
		ModelFastThinking:    a.ModelFastThinking,
		SystemPrompt:         a.SystemPrompt,
		Soul:                 a.Soul,
		Workspace:            a.Workspace,
		Sandbox:              sandboxJSON,
		EnabledBuiltinSkills: json.RawMessage("[]"),
		Scope:                scope,
		Enabled:              a.Enabled,
	})
	if err != nil {
		return fmt.Errorf("update agent %q: %w", a.ID, err)
	}
	return nil
}

func (s *DBStore) DeleteAgent(ctx context.Context, id string) error {
	err := s.q.DeleteAgent(ctx, id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23001" || pgErr.Code == "23503") && pgErr.ConstraintName == "webhook_agent_id_fkey" {
		return config.ErrAgentInUse
	}
	return err
}

// --- Channels ---

func (s *DBStore) ListChannels(ctx context.Context) ([]config.Channel, error) {
	rows, err := s.q.ListChannels(ctx)
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
	rows, err := s.q.ListChannelsByType(ctx, channelType)
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
	r, err := s.q.GetChannel(ctx, id)
	if err != nil {
		return config.Channel{}, fmt.Errorf("get channel %q: %w", id, err)
	}
	return channelFromDB(r), nil
}

func (s *DBStore) UpsertChannel(ctx context.Context, ch config.Channel) error {
	if ch.ID == "" {
		ch.ID = uuid.Must(uuid.NewV7()).String()
	}
	channelType := effectiveStoredChannelType(ch)
	err := s.q.UpsertChannel(ctx, sqlc.UpsertChannelParams{
		ID:      ch.ID,
		Name:    ch.Name,
		Type:    channelType,
		AgentID: pgtype.Text{String: ch.AgentID, Valid: ch.AgentID != ""},
		Enabled: ch.Enabled,
		Config:  ch.Config,
	})
	return s.channelWriteError(ctx, ch, channelType, err)
}

func (s *DBStore) CreateChannel(ctx context.Context, ch config.Channel) error {
	if ch.ID == "" {
		ch.ID = uuid.Must(uuid.NewV7()).String()
	}
	channelType := effectiveStoredChannelType(ch)
	_, err := s.q.CreateChannel(ctx, sqlc.CreateChannelParams{
		ID:      ch.ID,
		Name:    ch.Name,
		Type:    channelType,
		AgentID: pgtype.Text{String: ch.AgentID, Valid: ch.AgentID != ""},
		Enabled: ch.Enabled,
		Config:  ch.Config,
	})
	return s.channelWriteError(ctx, ch, channelType, err)
}

func (s *DBStore) UpdateChannel(ctx context.Context, ch config.Channel) error {
	channelType := effectiveStoredChannelType(ch)
	_, err := s.q.UpdateChannel(ctx, sqlc.UpdateChannelParams{
		ID:      ch.ID,
		Name:    ch.Name,
		Type:    channelType,
		AgentID: pgtype.Text{String: ch.AgentID, Valid: ch.AgentID != ""},
		Enabled: ch.Enabled,
		Config:  ch.Config,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return config.ErrChannelNotFound
	}
	return s.channelWriteError(ctx, ch, channelType, err)
}

func effectiveStoredChannelType(ch config.Channel) string {
	if ch.Type != "" {
		return ch.Type
	}
	return ch.ID
}

func (s *DBStore) channelWriteError(ctx context.Context, ch config.Channel, channelType string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "channel_pkey" {
		return config.ErrChannelExists
	}
	if !isChannelBindingViolation(err) {
		return err
	}
	rows, listErr := s.q.ListChannels(ctx)
	if listErr != nil {
		return err
	}
	for _, row := range rows {
		existing := channelFromDB(row)
		if existing.ID != ch.ID && existing.AgentID == ch.AgentID && existing.Type == channelType {
			return &config.ChannelBindingConflictError{AgentID: ch.AgentID, Type: channelType, ChannelID: existing.ID}
		}
	}
	return err
}

func isChannelBindingViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_channel_agent_id_type"
}

func (s *DBStore) DeleteChannel(ctx context.Context, id string) error {
	return s.q.DeleteChannel(ctx, id)
}

// --- Plugins ---

func (s *DBStore) ListPlugins(ctx context.Context) ([]config.Plugin, error) {
	return s.mergedPlugins(ctx, nil)
}

func (s *DBStore) ListPluginOverrides(ctx context.Context) ([]config.Plugin, error) {
	rows, err := s.q.ListPluginOverrides(ctx)
	if err != nil {
		return nil, fmt.Errorf("list plugin overrides: %w", err)
	}
	out := make([]config.Plugin, len(rows))
	for i, r := range rows {
		out[i] = pluginFromDB(r)
	}
	return out, nil
}

func (s *DBStore) ListPluginsByKind(ctx context.Context, kind string) ([]config.Plugin, error) {
	filter := func(p config.Plugin) bool { return p.Kind == kind }
	return s.mergedPlugins(ctx, filter)
}

func (s *DBStore) ListEnabledPlugins(ctx context.Context) ([]config.Plugin, error) {
	filter := func(p config.Plugin) bool { return p.Enabled }
	return s.mergedPlugins(ctx, filter)
}

func (s *DBStore) GetPlugin(ctx context.Context, id string) (config.Plugin, error) {
	builtin, isBuiltin := config.BuiltinPluginByID(id)
	r, dbErr := s.q.GetPlugin(ctx, id)
	if dbErr == nil {
		p := pluginFromDB(r)
		if isBuiltin {
			p.Kind = builtin.Kind
			p.Name = builtin.Name
		}
		return p, nil
	}
	if isBuiltin && errors.Is(dbErr, pgx.ErrNoRows) {
		return config.Plugin{
			ID:      builtin.ID,
			Kind:    builtin.Kind,
			Name:    builtin.Name,
			Enabled: builtin.DefaultEnabled,
			Config:  map[string]any{},
		}, nil
	}
	return config.Plugin{}, fmt.Errorf("get plugin %q: %w", id, dbErr)
}

func (s *DBStore) UpsertPlugin(ctx context.Context, p config.Plugin) error {
	configJSON, err := json.Marshal(p.Config)
	if err != nil {
		return fmt.Errorf("marshal plugin config %q: %w", p.ID, err)
	}
	return s.q.UpsertPlugin(ctx, sqlc.UpsertPluginParams{
		ID:      p.ID,
		Kind:    p.Kind,
		Name:    p.Name,
		Enabled: p.Enabled,
		Config:  configJSON,
	})
}

func (s *DBStore) SetPluginEnabled(ctx context.Context, id string, enabled bool) error {
	p, err := s.GetPlugin(ctx, id)
	if err != nil {
		return fmt.Errorf("set plugin enabled: %w", err)
	}
	p.Enabled = enabled
	return s.UpsertPlugin(ctx, p)
}

func (s *DBStore) SetPluginConfig(ctx context.Context, id string, cfg map[string]any) error {
	p, err := s.GetPlugin(ctx, id)
	if err != nil {
		return fmt.Errorf("set plugin config: %w", err)
	}
	p.Config = cfg
	return s.UpsertPlugin(ctx, p)
}

func (s *DBStore) DeletePlugin(ctx context.Context, id string) error {
	return s.q.DeletePlugin(ctx, id)
}

// --- Manifest plugin overrides ---

func (s *DBStore) GetManifestPluginOverride(ctx context.Context, pluginID string) (config.ManifestPluginOverride, bool, error) {
	row, err := s.q.GetManifestPluginOverride(ctx, pluginID)
	if errors.Is(err, pgx.ErrNoRows) {
		return config.ManifestPluginOverride{}, false, nil
	}
	if err != nil {
		return config.ManifestPluginOverride{}, false, fmt.Errorf("get manifest override %q: %w", pluginID, err)
	}
	return manifestOverrideFromDB(row), true, nil
}

func (s *DBStore) ListManifestPluginOverrides(ctx context.Context) ([]config.ManifestPluginOverride, error) {
	rows, err := s.q.ListManifestPluginOverrides(ctx)
	if err != nil {
		return nil, fmt.Errorf("list manifest overrides: %w", err)
	}
	out := make([]config.ManifestPluginOverride, len(rows))
	for i, r := range rows {
		out[i] = manifestOverrideFromDB(r)
	}
	return out, nil
}

func (s *DBStore) UpsertManifestPluginOverride(ctx context.Context, ov config.ManifestPluginOverride) error {
	var enabled pgtype.Bool
	if ov.Enabled != nil {
		enabled = pgtype.Bool{Bool: *ov.Enabled, Valid: true}
	}
	return s.q.UpsertManifestPluginOverride(ctx, sqlc.UpsertManifestPluginOverrideParams{
		PluginID:           ov.PluginID,
		Enabled:            enabled,
		SessionEnvVaultKey: ov.SessionEnvVaultKey,
		Config:             ov.Config,
	})
}

func (s *DBStore) DeleteManifestPluginOverride(ctx context.Context, pluginID string) error {
	return s.q.DeleteManifestPluginOverride(ctx, pluginID)
}

func manifestOverrideFromDB(r sqlc.PluginOverride) config.ManifestPluginOverride {
	out := config.ManifestPluginOverride{
		PluginID:           r.PluginID,
		SessionEnvVaultKey: r.SessionEnvVaultKey,
		Config:             r.Config,
		UpdatedAt:          r.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if r.Enabled.Valid {
		enabled := r.Enabled.Bool
		out.Enabled = &enabled
	}
	return out
}

// mergedPlugins returns builtins merged with DB overrides, optionally filtered.
func (s *DBStore) mergedPlugins(ctx context.Context, filter func(config.Plugin) bool) ([]config.Plugin, error) {
	rows, err := s.q.ListPluginOverrides(ctx)
	if err != nil {
		return nil, fmt.Errorf("list plugin overrides: %w", err)
	}
	overrides := make(map[string]sqlc.Plugin, len(rows))
	for _, r := range rows {
		overrides[r.ID] = r
	}

	var out []config.Plugin
	seen := make(map[string]bool)

	for _, b := range config.BuiltinPlugins() {
		p := config.Plugin{
			ID:      b.ID,
			Kind:    b.Kind,
			Name:    b.Name,
			Enabled: b.DefaultEnabled,
			Config:  map[string]any{},
		}
		if ov, ok := overrides[b.ID]; ok {
			p.Enabled = ov.Enabled
			var cfg map[string]any
			if len(ov.Config) > 0 && string(ov.Config) != "{}" {
				_ = json.Unmarshal(ov.Config, &cfg)
			}
			if cfg != nil {
				p.Config = cfg
			}
		}
		if filter == nil || filter(p) {
			out = append(out, p)
		}
		seen[b.ID] = true
	}

	for _, r := range rows {
		if seen[r.ID] {
			continue
		}
		p := pluginFromDB(r)
		if filter == nil || filter(p) {
			out = append(out, p)
		}
	}

	return out, nil
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
		return "", fmt.Errorf("get chat agent: %w", err)
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
		if errors.Is(err, pgx.ErrNoRows) {
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

func (s *DBStore) Snapshot(ctx context.Context, agentID string) (*config.Snapshot, error) {
	ag, err := s.q.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("snapshot: get agent %q: %w", agentID, err)
	}

	plugins, err := s.mergedPlugins(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("snapshot: list plugins: %w", err)
	}

	// The vision model is deployment-wide rather than per-agent, so it is read
	// from the singleton setting and then resolved alongside the agent's own
	// tiers — its provider credentials must be in the snapshot like any other.
	visionCfg, err := config.LoadVisionSettings(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("snapshot: load vision settings: %w", err)
	}

	providers, modelInputs, defaultCreds, err := s.resolveProviders(ctx, ag.Model, ag.ModelStrong, ag.ModelFast, visionCfg.Model)
	if err != nil {
		return nil, err
	}

	defaultProvID, _ := config.ParseModelRef(ag.Model)

	sandboxCfg, err := parseSandboxConfig(ag.Sandbox)
	if err != nil {
		return nil, fmt.Errorf("snapshot: parse agent sandbox config %q: %w", agentID, err)
	}

	snap := &config.Snapshot{
		AgentID:             agentID,
		Provider:            defaultProvID,
		Model:               ag.Model,
		ModelThinking:       ag.ModelThinking,
		ModelStrong:         ag.ModelStrong,
		ModelStrongThinking: ag.ModelStrongThinking,
		ModelFast:           ag.ModelFast,
		ModelFastThinking:   ag.ModelFastThinking,
		ModelVision:         visionCfg.Model,
		Workspace:           ag.Workspace,
		Sandbox:             sandboxCfg,
		APIKey:              defaultCreds.APIKey,
		BaseURL:             defaultCreds.BaseURL,
		SystemPrompt:        ag.SystemPrompt,
		Soul:                ag.Soul,
		Providers:           providers,
		ModelInputs:         modelInputs,
		Plugins:             plugins,
	}

	if val, err := s.GetSetting(ctx, "runner"); err == nil && val != "" {
		_ = json.Unmarshal([]byte(val), &snap.Runner)
	}
	if val, err := s.GetSetting(ctx, "compaction"); err == nil && val != "" {
		_ = json.Unmarshal([]byte(val), &snap.Compaction)
	}
	if val, err := s.GetSetting(ctx, "scheduler"); err == nil && val != "" {
		_ = json.Unmarshal([]byte(val), &snap.Scheduler)
	}
	if snap.Runner.IdleTimeout == 0 {
		snap.Runner.IdleTimeout = 10
	}

	return snap, nil
}

// resolveProviders returns the credentials for every provider referenced by the
// given model refs, the declared input modalities of those providers' models,
// and the credentials of the first ref's provider.
func (s *DBStore) resolveProviders(ctx context.Context, models ...string) (map[string]config.ProviderCreds, map[config.ModelKey][]string, config.ProviderCreds, error) {
	provIDs := collectProviderIDs(models...)
	rows, err := s.q.ListProviders(ctx)
	if err != nil {
		return nil, nil, config.ProviderCreds{}, fmt.Errorf("snapshot: list providers: %w", err)
	}

	byID := make(map[string]config.Provider, len(rows))
	typeCount := make(map[string]int)
	for _, row := range rows {
		p := providerFromDB(row)
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
	modelInputs := make(map[config.ModelKey][]string)
	for _, pid := range provIDs {
		p, ok := byID[pid]
		if !ok {
			continue
		}
		// p.ID is the canonical row ID even when pid is a type alias, so a per-Agent
		// override keyed by canonical ID can later be applied to every alias entry
		// that shares it.
		creds[pid] = config.ProviderCreds{Type: p.Type, APIKey: p.APIKey, BaseURL: p.BaseURL, ProviderID: p.ID}
		// Key by the referenced provider ID, not p.ID, so type aliases resolve
		// the same way the credentials above do.
		for modelID, m := range p.Models {
			if len(m.Input) > 0 {
				modelInputs[config.ModelKey{Provider: pid, Model: modelID}] = m.Input
			}
		}
	}

	var defaultModel string
	if len(models) > 0 {
		defaultModel = models[0]
	}
	defaultProvID, _ := config.ParseModelRef(defaultModel)
	defaultCreds := creds[defaultProvID]

	return creds, modelInputs, defaultCreds, nil
}

// --- Bootstrap ---

const defaultStellaSoul = `You are Stella — a sharp, efficient personal AI assistant.

- Warm but not chatty. Friendly but not performative.
- Lead with answers, not preamble.
- Match the user's energy: casual when they're casual, precise when they need precision.
- Own your mistakes quickly. No hedging or over-apologizing.
- Use humor sparingly and naturally — never forced.`

const defaultStellaAgentID = "stella"

// Seed removes legacy configuration and creates Stella only for an empty agent catalog.
func (s *DBStore) Seed(ctx context.Context) error {
	// The trace hook is no longer a plugin; drop any stale row from prior versions.
	if err := s.DeletePlugin(ctx, "hook/trace"); err != nil {
		return fmt.Errorf("seed: delete stale trace plugin: %w", err)
	}

	agents, err := s.q.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("seed: list agents: %w", err)
	}
	if len(agents) > 0 {
		return nil
	}
	workspace := filepath.Join(config.StellaHome(), "agents", defaultStellaAgentID)
	sandboxJSON, err := marshalSandboxConfig(config.SandboxConfig{})
	if err != nil {
		return fmt.Errorf("seed: marshal stella sandbox config: %w", err)
	}
	if err := s.q.SeedAgent(ctx, sqlc.SeedAgentParams{
		ID:                   defaultStellaAgentID,
		Name:                 "Stella",
		SystemPrompt:         defaultStellaSoul,
		Workspace:            workspace,
		Sandbox:              sandboxJSON,
		EnabledBuiltinSkills: json.RawMessage("[]"),
		Scope:                config.AgentScopeSystem,
		Enabled:              true,
	}); err != nil {
		return fmt.Errorf("seed: create stella agent: %w", err)
	}

	return nil
}

// --- Helpers ---

func marshalSandboxConfig(cfg config.SandboxConfig) (json.RawMessage, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal sandbox config: %w", err)
	}
	return data, nil
}

func parseSandboxConfig(raw json.RawMessage) (config.SandboxConfig, error) {
	var cfg config.SandboxConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return config.SandboxConfig{}, fmt.Errorf("parse sandbox config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return config.SandboxConfig{}, err
	}
	return cfg, nil
}

func providerFromDB(r sqlc.Provider) config.Provider {
	cfg := map[string]any{}
	if len(r.Config) > 0 {
		_ = json.Unmarshal(r.Config, &cfg)
	}
	apiKey, _ := cfg["api_key"].(string)
	baseURL, _ := cfg["base_url"].(string)
	return config.Provider{
		ID:      r.ID,
		Type:    r.Type,
		Name:    providerDisplayName(r.Name, r.ID),
		Enabled: r.Enabled,
		APIKey:  apiKey,
		BaseURL: baseURL,
		Models:  providerModelsFromAny(cfg["models"]),
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

func agentFromDB(r sqlc.Agent) (config.Agent, error) {
	scope := r.Scope
	if scope == "" {
		scope = config.AgentScopeSystem
	}
	sandboxCfg, err := parseSandboxConfig(r.Sandbox)
	if err != nil {
		return config.Agent{}, fmt.Errorf("parse agent %q sandbox config: %w", r.ID, err)
	}
	return config.Agent{
		ID:                  r.ID,
		Name:                r.Name,
		Model:               r.Model,
		ModelThinking:       r.ModelThinking,
		ModelStrong:         r.ModelStrong,
		ModelStrongThinking: r.ModelStrongThinking,
		ModelFast:           r.ModelFast,
		ModelFastThinking:   r.ModelFastThinking,
		SystemPrompt:        r.SystemPrompt,
		Soul:                r.Soul,
		Workspace:           r.Workspace,
		Sandbox:             sandboxCfg,
		Scope:               scope,
		CreatorID:           r.CreatorID,
		Enabled:             r.Enabled,
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

func pluginFromDB(r sqlc.Plugin) config.Plugin {
	var cfg map[string]any
	if len(r.Config) > 0 && string(r.Config) != "{}" {
		_ = json.Unmarshal(r.Config, &cfg)
	}
	if cfg == nil {
		cfg = make(map[string]any)
	}
	return config.Plugin{
		ID:      r.ID,
		Kind:    r.Kind,
		Name:    r.Name,
		Enabled: r.Enabled,
		Config:  cfg,
	}
}

func channelFromDB(r sqlc.Channel) config.Channel {
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
		Name:    r.Name,
		Type:    channelType,
		AgentID: agentID,
		Enabled: r.Enabled,
		Config:  r.Config,
	}
}
