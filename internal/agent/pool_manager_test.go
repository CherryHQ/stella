package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/memory"
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
			provID, _ := config.ParseModelRef(a.Model)
			p := m.providers[provID]
			return &config.Snapshot{
				Provider:  provID,
				Model:     a.Model,
				APIKey:    p.APIKey,
				BaseURL:   p.BaseURL,
				Workspace: a.Workspace,
				Runner:    config.RunnerConfig{Type: "go"},
				Providers: map[string]config.ProviderCreds{
					provID: {APIKey: p.APIKey, BaseURL: p.BaseURL},
				},
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
func (m *mockStore) GetAgent(_ context.Context, id string) (config.Agent, error) {
	for _, a := range m.agents {
		if a.ID == id {
			return a, nil
		}
	}
	return config.Agent{}, fmt.Errorf("agent %q not found", id)
}
func (m *mockStore) CreateAgent(_ context.Context, _ config.Agent) error      { return nil }
func (m *mockStore) UpdateAgent(_ context.Context, _ config.Agent) error      { return nil }
func (m *mockStore) DeleteAgent(_ context.Context, _ string) error            { return nil }
func (m *mockStore) ListChannels(_ context.Context) ([]config.Channel, error) { return nil, nil }
func (m *mockStore) ListChannelsByType(context.Context, string) ([]config.Channel, error) {
	return nil, nil
}

func (m *mockStore) GetChannel(_ context.Context, _ string) (config.Channel, error) {
	return config.Channel{}, nil
}
func (m *mockStore) UpsertChannel(_ context.Context, _ config.Channel) error { return nil }
func (m *mockStore) DeleteChannel(_ context.Context, _ string) error         { return nil }
func (m *mockStore) GetChatAgent(_ context.Context, _, _, _ string) (string, error) {
	return "", nil
}
func (m *mockStore) SetChatAgent(_ context.Context, _, _, _, _ string) error { return nil }
func (m *mockStore) DeleteChatAgent(_ context.Context, _, _, _ string) error { return nil }
func (m *mockStore) GetSetting(_ context.Context, _ string) (string, error)  { return "", nil }
func (m *mockStore) SetSetting(_ context.Context, _, _ string) error         { return nil }
func (m *mockStore) SeedDefaults(_ context.Context) error                    { return nil }
func (m *mockStore) ListPlugins(_ context.Context) ([]config.Plugin, error)  { return nil, nil }
func (m *mockStore) ListPluginsByKind(_ context.Context, _ string) ([]config.Plugin, error) {
	return nil, nil
}
func (m *mockStore) ListEnabledPlugins(_ context.Context) ([]config.Plugin, error) { return nil, nil }
func (m *mockStore) GetPlugin(_ context.Context, _ string) (config.Plugin, error) {
	return config.Plugin{}, nil
}
func (m *mockStore) UpsertPlugin(_ context.Context, _ config.Plugin) error               { return nil }
func (m *mockStore) SetPluginEnabled(_ context.Context, _ string, _ bool) error          { return nil }
func (m *mockStore) SetPluginConfig(_ context.Context, _ string, _ map[string]any) error { return nil }
func (m *mockStore) DeletePlugin(_ context.Context, _ string) error                      { return nil }

func TestPoolManagerGetNil(t *testing.T) {
	store := &mockStore{}
	mem := testMemoryProvider(t)
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
			{ID: "anna", Name: "Anna", Model: "anthropic/test-model", Enabled: true},
			{ID: "coder", Name: "Coder", Model: "anthropic/test-model", Enabled: true},
			{ID: "disabled", Name: "Disabled", Model: "anthropic/test-model", Enabled: false},
		},
		providers: map[string]config.Provider{
			"anthropic": {ID: "anthropic", APIKey: "test-key"},
		},
	}

	mem := testMemoryProvider(t)
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
			{ID: "anna", Name: "Anna", Model: "anthropic/test-model", Enabled: true},
		},
		providers: map[string]config.Provider{
			"anthropic": {ID: "anthropic", APIKey: "test-key"},
		},
	}

	mem := testMemoryProvider(t)
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
			{ID: "anna", Name: "Anna", Model: "anthropic/test-model", Enabled: true},
		},
		providers: map[string]config.Provider{
			"anthropic": {ID: "anthropic", APIKey: "test-key"},
		},
	}

	mem := testMemoryProvider(t)
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

type closeCountingMemory struct {
	memory.Provider
	closeCount int
}

func (m *closeCountingMemory) Close() error {
	m.closeCount++
	return m.Provider.Close()
}

func TestPoolManagerSyncAgentRemovalKeepsSharedMemoryOpen(t *testing.T) {
	t.Setenv("ANNA_HOME", t.TempDir())
	config.ResetAnnaHome()
	t.Cleanup(config.ResetAnnaHome)

	store := &mockStore{
		agents: []config.Agent{
			{ID: "anna", Name: "Anna", Model: "anthropic/test-model", Enabled: true},
			{ID: "coder", Name: "Coder", Model: "anthropic/test-model", Enabled: true},
		},
		providers: map[string]config.Provider{
			"anthropic": {ID: "anthropic", APIKey: "test-key"},
		},
	}

	mem := &closeCountingMemory{Provider: testMemoryProvider(t)}
	pm := NewPoolManager(store, mem)

	ctx := context.Background()
	if err := pm.StartAll(ctx); err != nil {
		t.Fatalf("StartAll: %v", err)
	}

	store.agents[0].Enabled = false
	if err := pm.SyncAgent(ctx, "anna"); err != nil {
		t.Fatalf("SyncAgent: %v", err)
	}
	if mem.closeCount != 0 {
		t.Fatalf("memory close count after removing one pool = %d, want 0", mem.closeCount)
	}
	if got := pm.Get("anna"); got != nil {
		t.Fatal("Get(anna) should be nil after disabled agent sync")
	}
	if got := pm.Get("coder"); got == nil {
		t.Fatal("Get(coder) should remain available")
	}

	if err := pm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if mem.closeCount != 1 {
		t.Fatalf("memory close count after manager close = %d, want 1", mem.closeCount)
	}
}

func TestPoolManagerStartAllNoAgents(t *testing.T) {
	store := &mockStore{agents: nil}
	mem := testMemoryProvider(t)
	pm := NewPoolManager(store, mem)

	err := pm.StartAll(context.Background())
	if err == nil {
		t.Fatal("expected error for no agents")
	}
}
