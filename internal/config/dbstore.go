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

// --- Providers ---

func (s *DBStore) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.q.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	out := make([]Provider, len(rows))
	for i, r := range rows {
		out[i] = providerFromDB(r)
	}
	return out, nil
}

func (s *DBStore) GetProvider(ctx context.Context, id string) (Provider, error) {
	r, err := s.q.GetProvider(ctx, id)
	if err != nil {
		return Provider{}, fmt.Errorf("get provider %q: %w", id, err)
	}
	p := providerFromDB(r)
	// Env var fallback for known providers.
	applyProviderEnvFallback(&p)
	return p, nil
}

func (s *DBStore) CreateProvider(ctx context.Context, p Provider) error {
	_, err := s.q.CreateProvider(ctx, sqlc.CreateProviderParams{
		ID:      p.ID,
		Name:    p.Name,
		ApiKey:  p.APIKey,
		BaseUrl: p.BaseURL,
	})
	if err != nil {
		return fmt.Errorf("create provider %q: %w", p.ID, err)
	}
	return nil
}

func (s *DBStore) UpdateProvider(ctx context.Context, p Provider) error {
	err := s.q.UpdateProvider(ctx, sqlc.UpdateProviderParams{
		ID:      p.ID,
		Name:    p.Name,
		ApiKey:  p.APIKey,
		BaseUrl: p.BaseURL,
	})
	if err != nil {
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
	_, err := s.q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID:           a.ID,
		Name:         a.Name,
		ProviderID:   a.ProviderID,
		Model:        a.Model,
		ModelStrong:  a.ModelStrong,
		ModelFast:    a.ModelFast,
		SystemPrompt: a.SystemPrompt,
		Workspace:    a.Workspace,
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
	err := s.q.UpdateAgent(ctx, sqlc.UpdateAgentParams{
		ID:           a.ID,
		Name:         a.Name,
		ProviderID:   a.ProviderID,
		Model:        a.Model,
		ModelStrong:  a.ModelStrong,
		ModelFast:    a.ModelFast,
		SystemPrompt: a.SystemPrompt,
		Workspace:    a.Workspace,
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

// --- Users ---

func (s *DBStore) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.q.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	out := make([]User, len(rows))
	for i, r := range rows {
		out[i] = userFromDB(r)
	}
	return out, nil
}

func (s *DBStore) GetUser(ctx context.Context, id int64) (User, error) {
	r, err := s.q.GetUser(ctx, id)
	if err != nil {
		return User{}, fmt.Errorf("get user %d: %w", id, err)
	}
	return userFromDB(r), nil
}

func (s *DBStore) UpsertUser(ctx context.Context, externalID, platform, name string) (User, error) {
	r, err := s.q.UpsertUser(ctx, sqlc.UpsertUserParams{
		ExternalID: externalID,
		Platform:   platform,
		Name:       name,
	})
	if err != nil {
		return User{}, fmt.Errorf("upsert user: %w", err)
	}
	return userFromDB(r), nil
}

func (s *DBStore) UpdateUserDefaultAgent(ctx context.Context, userID int64, agentID string) error {
	return s.q.UpdateUserDefaultAgent(ctx, sqlc.UpdateUserDefaultAgentParams{
		ID:             userID,
		DefaultAgentID: sql.NullString{String: agentID, Valid: agentID != ""},
	})
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

	prov, err := s.q.GetProvider(ctx, ag.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("snapshot: get provider %q: %w", ag.ProviderID, err)
	}
	p := providerFromDB(prov)
	applyProviderEnvFallback(&p)

	snap := &Snapshot{
		Provider:     p.ID,
		Model:        ag.Model,
		ModelStrong:  ag.ModelStrong,
		ModelFast:    ag.ModelFast,
		Workspace:    ag.Workspace,
		APIKey:       p.APIKey,
		BaseURL:      p.BaseURL,
		SystemPrompt: ag.SystemPrompt,
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
	if val, err := s.GetSetting(ctx, "plugins"); err == nil && val != "" {
		_ = json.Unmarshal([]byte(val), &snap.Plugins)
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
	// Seed default provider if none exist.
	providers, err := s.q.ListProviders(ctx)
	if err != nil {
		return fmt.Errorf("seed: list providers: %w", err)
	}
	if len(providers) == 0 {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		_, err := s.q.CreateProvider(ctx, sqlc.CreateProviderParams{
			ID:     "anthropic",
			Name:   "Anthropic",
			ApiKey: apiKey,
		})
		if err != nil {
			return fmt.Errorf("seed: create anthropic provider: %w", err)
		}
	}

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
			ProviderID:   "anthropic",
			Model:        "claude-sonnet-4-6",
			SystemPrompt: defaultAnnaSoul,
			Workspace:    workspace,
			Enabled:      1,
		})
		if err != nil {
			return fmt.Errorf("seed: create anna agent: %w", err)
		}
	}

	return nil
}

// --- Helpers ---

func providerFromDB(r sqlc.Provider) Provider {
	return Provider{
		ID:      r.ID,
		Name:    r.Name,
		APIKey:  r.ApiKey,
		BaseURL: r.BaseUrl,
	}
}

// applyProviderEnvFallback fills empty API key and base URL from environment
// variables for known provider slugs.
func applyProviderEnvFallback(p *Provider) {
	switch p.ID {
	case "anthropic":
		envFallback(&p.APIKey, "ANTHROPIC_API_KEY")
		envFallback(&p.BaseURL, "ANTHROPIC_BASE_URL")
	case "openai", "openai-response":
		envFallback(&p.APIKey, "OPENAI_API_KEY")
		envFallback(&p.BaseURL, "OPENAI_BASE_URL")
	}
}

func envFallback(dst *string, envKey string) {
	if *dst == "" {
		if v := os.Getenv(envKey); v != "" {
			*dst = v
		}
	}
}

func agentFromDB(r sqlc.Agent) Agent {
	return Agent{
		ID:           r.ID,
		Name:         r.Name,
		ProviderID:   r.ProviderID,
		Model:        r.Model,
		ModelStrong:  r.ModelStrong,
		ModelFast:    r.ModelFast,
		SystemPrompt: r.SystemPrompt,
		Workspace:    r.Workspace,
		Enabled:      r.Enabled == 1,
	}
}

func channelFromDB(r sqlc.Channel) Channel {
	return Channel{
		ID:      r.ID,
		Enabled: r.Enabled == 1,
		Config:  r.Config,
	}
}

func userFromDB(r sqlc.User) User {
	return User{
		ID:             r.ID,
		ExternalID:     r.ExternalID,
		Platform:       r.Platform,
		Name:           r.Name,
		DefaultAgentID: r.DefaultAgentID.String,
	}
}
