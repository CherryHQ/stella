package agent

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/config"
)

// mockStore implements config.Store for testing PoolManager.
type mockStore struct {
	agents    []config.Agent
	providers map[string]config.Provider
}

func (m *mockStore) ListEnabledAgents(_ context.Context) ([]config.Agent, error) {
	var enabled []config.Agent
	for _, a := range m.agents {
		if a.Enabled {
			enabled = append(enabled, a)
		}
	}
	return enabled, nil
}

func (m *mockStore) Snapshot(_ context.Context, agentID string) (*config.Snapshot, error) {
	for _, a := range m.agents {
		if a.ID == agentID {
			p := m.providers[a.ProviderID]
			return &config.Snapshot{
				Provider:  a.ProviderID,
				Model:     a.Model,
				APIKey:    p.APIKey,
				BaseURL:   p.BaseURL,
				Workspace: a.Workspace,
				Runner:    config.RunnerConfig{Type: "go"},
			}, nil
		}
	}
	return nil, nil
}

// Stub out all other Store methods to satisfy the interface.
func (m *mockStore) ListProviders(_ context.Context) ([]config.Provider, error) { return nil, nil }
func (m *mockStore) GetProvider(_ context.Context, _ string) (config.Provider, error) {
	return config.Provider{}, nil
}
func (m *mockStore) CreateProvider(_ context.Context, _ config.Provider) error { return nil }
func (m *mockStore) UpdateProvider(_ context.Context, _ config.Provider) error { return nil }
func (m *mockStore) DeleteProvider(_ context.Context, _ string) error          { return nil }
func (m *mockStore) ListAgents(_ context.Context) ([]config.Agent, error)      { return nil, nil }
func (m *mockStore) GetAgent(_ context.Context, _ string) (config.Agent, error) {
	return config.Agent{}, nil
}
func (m *mockStore) CreateAgent(_ context.Context, _ config.Agent) error      { return nil }
func (m *mockStore) UpdateAgent(_ context.Context, _ config.Agent) error      { return nil }
func (m *mockStore) DeleteAgent(_ context.Context, _ string) error            { return nil }
func (m *mockStore) ListChannels(_ context.Context) ([]config.Channel, error) { return nil, nil }
func (m *mockStore) GetChannel(_ context.Context, _ string) (config.Channel, error) {
	return config.Channel{}, nil
}
func (m *mockStore) UpsertChannel(_ context.Context, _ config.Channel) error { return nil }
func (m *mockStore) ListUsers(_ context.Context) ([]config.User, error)      { return nil, nil }
func (m *mockStore) GetUser(_ context.Context, _ int64) (config.User, error) {
	return config.User{}, nil
}
func (m *mockStore) UpsertUser(_ context.Context, _, _, _ string) (config.User, error) {
	return config.User{}, nil
}
func (m *mockStore) UpdateUserDefaultAgent(_ context.Context, _ int64, _ string) error { return nil }
func (m *mockStore) GetChatAgent(_ context.Context, _, _ string) (string, error)       { return "", nil }
func (m *mockStore) SetChatAgent(_ context.Context, _, _, _ string) error              { return nil }
func (m *mockStore) DeleteChatAgent(_ context.Context, _, _ string) error              { return nil }
func (m *mockStore) GetUserAgentMemory(_ context.Context, _ int64, _ string) (string, error) {
	return "", nil
}
func (m *mockStore) SetUserAgentMemory(_ context.Context, _ int64, _, _ string) error { return nil }
func (m *mockStore) GetSetting(_ context.Context, _ string) (string, error)           { return "", nil }
func (m *mockStore) SetSetting(_ context.Context, _, _ string) error                  { return nil }
func (m *mockStore) SeedDefaults(_ context.Context) error                             { return nil }

func TestPoolManagerGetNil(t *testing.T) {
	store := &mockStore{}
	mem := testMemoryEngine(t)
	pm := NewPoolManager(store, mem)

	if got := pm.Get("nonexistent"); got != nil {
		t.Errorf("Get(nonexistent) = %v, want nil", got)
	}
}

func TestPoolManagerStartAllAndGet(t *testing.T) {
	t.Setenv("ANNA_HOME", t.TempDir())
	config.ResetAnnaHome()
	t.Cleanup(config.ResetAnnaHome)

	store := &mockStore{
		agents: []config.Agent{
			{ID: "anna", Name: "Anna", ProviderID: "anthropic", Model: "test-model", Enabled: true},
			{ID: "coder", Name: "Coder", ProviderID: "anthropic", Model: "test-model", Enabled: true},
			{ID: "disabled", Name: "Disabled", ProviderID: "anthropic", Model: "test-model", Enabled: false},
		},
		providers: map[string]config.Provider{
			"anthropic": {ID: "anthropic", APIKey: "test-key"},
		},
	}

	mem := testMemoryEngine(t)
	pm := NewPoolManager(store, mem)

	ctx := context.Background()
	if err := pm.StartAll(ctx); err != nil {
		t.Fatalf("StartAll: %v", err)
	}
	defer func() { _ = pm.Close() }()

	// Should have pools for enabled agents.
	if p := pm.Get("anna"); p == nil {
		t.Error("Get(anna) = nil, want non-nil")
	} else if p.AgentID() != "anna" {
		t.Errorf("AgentID() = %q, want %q", p.AgentID(), "anna")
	}

	if p := pm.Get("coder"); p == nil {
		t.Error("Get(coder) = nil, want non-nil")
	}

	// Disabled agent should not have a pool.
	if p := pm.Get("disabled"); p != nil {
		t.Error("Get(disabled) should be nil for disabled agent")
	}
}

func TestPoolManagerDefaultPool(t *testing.T) {
	t.Setenv("ANNA_HOME", t.TempDir())
	config.ResetAnnaHome()
	t.Cleanup(config.ResetAnnaHome)

	store := &mockStore{
		agents: []config.Agent{
			{ID: "anna", Name: "Anna", ProviderID: "anthropic", Model: "test-model", Enabled: true},
		},
		providers: map[string]config.Provider{
			"anthropic": {ID: "anthropic", APIKey: "test-key"},
		},
	}

	mem := testMemoryEngine(t)
	pm := NewPoolManager(store, mem)

	ctx := context.Background()
	if err := pm.StartAll(ctx); err != nil {
		t.Fatalf("StartAll: %v", err)
	}
	defer func() { _ = pm.Close() }()

	dp := pm.DefaultPool()
	if dp == nil {
		t.Fatal("DefaultPool() = nil, want non-nil")
	}
}

func TestPoolManagerClose(t *testing.T) {
	t.Setenv("ANNA_HOME", t.TempDir())
	config.ResetAnnaHome()
	t.Cleanup(config.ResetAnnaHome)

	store := &mockStore{
		agents: []config.Agent{
			{ID: "anna", Name: "Anna", ProviderID: "anthropic", Model: "test-model", Enabled: true},
		},
		providers: map[string]config.Provider{
			"anthropic": {ID: "anthropic", APIKey: "test-key"},
		},
	}

	mem := testMemoryEngine(t)
	pm := NewPoolManager(store, mem)

	ctx := context.Background()
	if err := pm.StartAll(ctx); err != nil {
		t.Fatalf("StartAll: %v", err)
	}

	if err := pm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After close, pools should be empty.
	if p := pm.Get("anna"); p != nil {
		t.Error("Get(anna) should be nil after Close")
	}
}

func TestPoolManagerStartAllNoAgents(t *testing.T) {
	store := &mockStore{agents: nil}
	mem := testMemoryEngine(t)
	pm := NewPoolManager(store, mem)

	err := pm.StartAll(context.Background())
	if err == nil {
		t.Fatal("expected error for no agents")
	}
}
