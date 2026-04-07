package pluginhost

import (
	"context"
	"sort"
	"testing"

	"github.com/vaayne/anna/internal/config"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type stubStore struct{ plugins map[string]config.Plugin }

func (s *stubStore) ListProviders(context.Context) ([]config.Provider, error) { return nil, nil }
func (s *stubStore) GetProvider(context.Context, string) (config.Provider, error) {
	return config.Provider{}, nil
}
func (s *stubStore) CreateProvider(context.Context, config.Provider) error     { return nil }
func (s *stubStore) UpdateProvider(context.Context, config.Provider) error     { return nil }
func (s *stubStore) DeleteProvider(context.Context, string) error              { return nil }
func (s *stubStore) ListAgents(context.Context) ([]config.Agent, error)        { return nil, nil }
func (s *stubStore) ListEnabledAgents(context.Context) ([]config.Agent, error) { return nil, nil }
func (s *stubStore) GetAgent(context.Context, string) (config.Agent, error) {
	return config.Agent{}, nil
}
func (s *stubStore) CreateAgent(context.Context, config.Agent) error        { return nil }
func (s *stubStore) UpdateAgent(context.Context, config.Agent) error        { return nil }
func (s *stubStore) DeleteAgent(context.Context, string) error              { return nil }
func (s *stubStore) ListChannels(context.Context) ([]config.Channel, error) { return nil, nil }
func (s *stubStore) GetChannel(context.Context, string) (config.Channel, error) {
	return config.Channel{}, nil
}
func (s *stubStore) UpsertChannel(context.Context, config.Channel) error { return nil }
func (s *stubStore) ListPlugins(context.Context) ([]config.Plugin, error) {
	plugins := make([]config.Plugin, 0, len(s.plugins))
	for _, plugin := range s.plugins {
		plugins = append(plugins, plugin)
	}
	sort.Slice(plugins, func(i, j int) bool {
		if plugins[i].Kind != plugins[j].Kind {
			return plugins[i].Kind < plugins[j].Kind
		}
		return plugins[i].Name < plugins[j].Name
	})
	return plugins, nil
}
func (s *stubStore) ListPluginsByKind(_ context.Context, kind string) ([]config.Plugin, error) {
	plugins := make([]config.Plugin, 0, len(s.plugins))
	for _, plugin := range s.plugins {
		if plugin.Kind == kind {
			plugins = append(plugins, plugin)
		}
	}
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].Name < plugins[j].Name })
	return plugins, nil
}
func (s *stubStore) ListEnabledPlugins(context.Context) ([]config.Plugin, error) { return nil, nil }
func (s *stubStore) GetPlugin(_ context.Context, id string) (config.Plugin, error) {
	return s.plugins[id], nil
}
func (s *stubStore) UpsertPlugin(context.Context, config.Plugin) error { return nil }
func (s *stubStore) SetPluginEnabled(_ context.Context, id string, enabled bool) error {
	p := s.plugins[id]
	p.Enabled = enabled
	s.plugins[id] = p
	return nil
}
func (s *stubStore) SetPluginConfig(_ context.Context, id string, cfg map[string]any) error {
	p := s.plugins[id]
	p.Config = cfg
	s.plugins[id] = p
	return nil
}
func (s *stubStore) DeletePlugin(context.Context, string) error                   { return nil }
func (s *stubStore) GetChatAgent(context.Context, string, string) (string, error) { return "", nil }
func (s *stubStore) SetChatAgent(context.Context, string, string, string) error   { return nil }
func (s *stubStore) DeleteChatAgent(context.Context, string, string) error        { return nil }
func (s *stubStore) GetSetting(context.Context, string) (string, error)           { return "", nil }
func (s *stubStore) SetSetting(context.Context, string, string) error             { return nil }
func (s *stubStore) Snapshot(context.Context, string) (*config.Snapshot, error)   { return nil, nil }
func (s *stubStore) SeedDefaults(context.Context) error                           { return nil }

func TestConfigServiceLegacyAlias(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{"tool/mcp": {ID: "tool/mcp", Enabled: true, Config: map[string]any{"x": 1}}}}
	host := New(store)
	host.RegisterLegacyID("mcp", "tool/mcp")
	state, err := host.DesiredState(context.Background(), "mcp")
	if err != nil {
		t.Fatal(err)
	}
	if state.ID != "mcp" || !state.Enabled || state.Config["x"] != 1 {
		t.Fatalf("bad state: %#v", state)
	}
}

func TestPromptToolsUsesCanonicalResolution(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{}}
	host := New(store)
	host.RegisterPluginID("mcp")
	host.RegisterPromptInventory(pkgplugins.PromptInventoryRegistration{PluginID: "mcp", Name: "tools", GetTools: func(context.Context) ([]pkgplugins.PromptToolInfo, error) {
		return []pkgplugins.PromptToolInfo{{Name: "mcp__docs__search"}}, nil
	}})
	tools, err := host.PromptTools(context.Background(), "mcp")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "mcp__docs__search" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
}

func TestRuntimeApplyCreatesAndApplies(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{"mcp": {ID: "mcp", Enabled: true, Config: map[string]any{"x": 1}}}}
	host := New(store)
	host.RegisterPluginID("mcp")
	called := 0
	host.RegisterRuntime(pkgplugins.RuntimeRegistration{PluginID: "mcp", Name: "main", Factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
		return runtimeStub{apply: func(_ context.Context, desired pkgplugins.PluginState) error {
			called++
			if desired.ID != "mcp" {
				t.Fatal(desired.ID)
			}
			return nil
		}}, nil
	}})
	if err := host.ApplyPlugin(context.Background(), "mcp"); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("expected 1 apply, got %d", called)
	}
}

type runtimeStub struct {
	apply func(context.Context, pkgplugins.PluginState) error
}

func (r runtimeStub) Apply(ctx context.Context, desired pkgplugins.PluginState) error {
	return r.apply(ctx, desired)
}
func (r runtimeStub) Stop(context.Context) error { return nil }
func (r runtimeStub) Snapshot(context.Context) (pkgplugins.RuntimeSnapshot, error) {
	return pkgplugins.RuntimeSnapshot{State: pkgplugins.RuntimeStateRunning}, nil
}
