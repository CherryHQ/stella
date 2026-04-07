package telegram

import (
	"context"
	"testing"

	internalchannel "github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/pluginhost"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func TestSelfRegisteredTelegramPluginIsComplete(t *testing.T) {
	host := pluginhost.New(newStubStore())
	services := pluginhost.NewChannelRuntimeServices()
	services.Set(context.Background(), fakeChannelHandler{}, internalchannel.NewDispatcher())
	host.SetChannelRuntimeServices(services)

	if err := host.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}
	if !host.HasRuntime(PluginID) || !host.HasConfig(PluginID) || !host.HasStatus(PluginID) {
		t.Fatal("expected telegram runtime/config/status registrations")
	}
	metas := host.ListRegisteredPlugins()
	found := false
	for _, meta := range metas {
		if meta.ID != PluginID {
			continue
		}
		found = true
		if meta.Kind != config.PluginKindChannel || meta.Name != internalchannel.PlatformTelegram || !meta.Managed || !meta.AdminVisible {
			t.Fatalf("unexpected telegram metadata: %#v", meta)
		}
	}
	if !found {
		t.Fatal("expected telegram metadata")
	}
}

func TestSelfRegisteredTelegramPluginAppliesAndReportsStatus(t *testing.T) {
	store := newStubStore()
	store.plugins[PluginID] = config.Plugin{ID: PluginID, Kind: config.PluginKindChannel, Name: internalchannel.PlatformTelegram, Enabled: true, Config: map[string]any{"token": "tg-token", "enable_notify": true}}

	host := pluginhost.New(store)
	services := pluginhost.NewChannelRuntimeServices()
	dispatcher := internalchannel.NewDispatcher()
	services.Set(context.Background(), fakeChannelHandler{}, dispatcher)
	host.SetChannelRuntimeServices(services)

	reset := SetRuntimeFactoryForTesting(func(host pkgplugins.ServiceHost) (pkgplugins.ManagedRuntime, error) {
		return NewManagedRuntime(RuntimeDeps{
			Parent:   context.Background(),
			Handler:  fakeChannelHandler{},
			Notifier: dispatcher,
			NewChannel: func(cfg internalchannel.TelegramConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
				return newTestChannel(), nil
			},
		}), nil
	})
	defer reset()

	if err := host.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}
	if err := host.ApplyPlugin(context.Background(), PluginID); err != nil {
		t.Fatalf("ApplyPlugin: %v", err)
	}
	status, err := host.Status(context.Background(), PluginID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	payload, ok := status.(map[string]any)
	if !ok {
		t.Fatalf("status type = %T", status)
	}
	if payload["state"] != pkgplugins.RuntimeStateRunning {
		t.Fatalf("state = %#v, want %q", payload["state"], pkgplugins.RuntimeStateRunning)
	}
	if got := dispatcher.Channels(); len(got) != 1 || got[0] != internalchannel.PlatformTelegram {
		t.Fatalf("dispatcher channels = %v", got)
	}
}

type stubStore struct{ plugins map[string]config.Plugin }

func newStubStore() *stubStore { return &stubStore{plugins: map[string]config.Plugin{}} }

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
	return plugins, nil
}
func (s *stubStore) ListPluginsByKind(context.Context, string) ([]config.Plugin, error) {
	return nil, nil
}
func (s *stubStore) ListEnabledPlugins(context.Context) ([]config.Plugin, error) { return nil, nil }
func (s *stubStore) GetPlugin(_ context.Context, id string) (config.Plugin, error) {
	return s.plugins[id], nil
}
func (s *stubStore) UpsertPlugin(_ context.Context, plugin config.Plugin) error {
	s.plugins[plugin.ID] = plugin
	return nil
}
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

type testChannel struct{ stop chan struct{} }

func newTestChannel() *testChannel                                         { return &testChannel{stop: make(chan struct{})} }
func (*testChannel) Name() string                                          { return internalchannel.PlatformTelegram }
func (c *testChannel) Start(ctx context.Context) error                     { <-ctx.Done(); close(c.stop); return ctx.Err() }
func (*testChannel) Stop()                                                 {}
func (*testChannel) Notify(context.Context, pkgchannel.Notification) error { return nil }
