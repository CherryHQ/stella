package pluginhost

import (
	"context"
	"testing"

	internalchannel "github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	internalreflect "github.com/vaayne/anna/internal/reflect"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgmcp "github.com/vaayne/anna/pkg/mcp"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	_ "github.com/vaayne/anna/plugins/tools/mcp"
)

func TestRegisterMetadataPanicsOnDuplicate(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	host.RegisterPluginID("channel/telegram")
	host.RegisterMetadata(pkgplugins.PluginMeta{
		ID:          "channel/telegram",
		Kind:        config.PluginKindChannel,
		Name:        "telegram",
		DisplayName: "Telegram",
	})

	defer func() {
		if recover() == nil {
			t.Fatal("expected duplicate metadata registration to panic")
		}
	}()

	host.RegisterMetadata(pkgplugins.PluginMeta{
		ID:          "channel/telegram",
		Kind:        config.PluginKindChannel,
		Name:        "telegram",
		DisplayName: "Telegram",
	})
}

func TestLoadCatalogFailsOnIncompleteManagedMetadata(t *testing.T) {
	catalog := pkgplugins.NewCatalog()
	catalog.Register("channel/telegram", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.Registry().RegisterMetadata(pkgplugins.PluginMeta{
			ID:          "channel/telegram",
			Kind:        config.PluginKindChannel,
			Name:        "telegram",
			DisplayName: "Telegram",
			Managed:     true,
			HasConfig:   true,
			HasStatus:   true,
			Capabilities: []string{
				pkgplugins.CapabilityChannel,
				pkgplugins.CapabilityRuntime,
				pkgplugins.CapabilityConfig,
				pkgplugins.CapabilityStatus,
			},
		})
	}))

	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	if err := host.LoadCatalog(catalog); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestDiscoveryReportsRegistrationsAndMergedAdminView(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{
		"channel/telegram": {
			ID:      "channel/telegram",
			Kind:    config.PluginKindChannel,
			Name:    "telegram",
			Enabled: true,
			Config:  map[string]any{"token": "abc"},
		},
		"provider/custom": {
			ID:      "provider/custom",
			Kind:    config.PluginKindProvider,
			Name:    "custom",
			Enabled: true,
			Config:  map[string]any{"api_key": "secret"},
		},
	}}
	host := New(store)
	host.RegisterPluginID("channel/telegram")
	host.RegisterMetadata(pkgplugins.PluginMeta{
		ID:                    "channel/telegram",
		Kind:                  config.PluginKindChannel,
		Name:                  "telegram",
		DisplayName:           "Telegram",
		Managed:               true,
		AdminVisible:          true,
		HasConfig:             true,
		HasStatus:             true,
		SupportsNotifications: true,
		Capabilities: []string{
			pkgplugins.CapabilityChannel,
			pkgplugins.CapabilityRuntime,
			pkgplugins.CapabilityConfig,
			pkgplugins.CapabilityStatus,
		},
	})
	host.RegisterConfig(pkgplugins.ConfigRegistration{PluginID: "channel/telegram", DefaultConfig: func() map[string]any { return map[string]any{"token": ""} }})
	host.RegisterChannel(pkgplugins.ChannelRegistration{PluginID: "channel/telegram", Name: "telegram"})
	host.RegisterRuntime(pkgplugins.RuntimeRegistration{PluginID: "channel/telegram", Name: "bot", Factory: func(pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
		return runtimeStub{apply: func(context.Context, pkgplugins.PluginState) error { return nil }}, nil
	}})
	host.RegisterStatus(pkgplugins.StatusRegistration{PluginID: "channel/telegram", Get: func(context.Context) (any, error) { return map[string]any{}, nil }})
	if err := host.ValidateRegistrations(); err != nil {
		t.Fatalf("ValidateRegistrations: %v", err)
	}

	if got := host.PluginsByKind(config.PluginKindChannel); len(got) != 1 || got[0] != "channel/telegram" {
		t.Fatalf("unexpected channel plugins: %#v", got)
	}
	if got := host.ManagedPlugins(); len(got) != 1 || got[0] != "channel/telegram" {
		t.Fatalf("unexpected managed plugins: %#v", got)
	}
	if !host.HasRuntime("channel/telegram") || !host.HasConfig("channel/telegram") || !host.HasStatus("channel/telegram") {
		t.Fatal("expected runtime/config/status registrations")
	}

	plugins, err := host.ListAdminVisiblePlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 2 {
		t.Fatalf("expected 2 plugins, got %#v", plugins)
	}
	if plugins[0].Meta.ID != "channel/telegram" || !plugins[0].Persisted || !plugins[0].State.Enabled || plugins[0].State.Config["token"] != "abc" {
		t.Fatalf("unexpected registered overlay entry: %#v", plugins[0])
	}
	if plugins[1].Meta.ID != "provider/custom" || !plugins[1].Persisted || plugins[1].PersistedID != "provider/custom" {
		t.Fatalf("unexpected persisted-only entry: %#v", plugins[1])
	}
}

func TestAdminVisibleDiscoveryIncludesBuiltinsBeforePersistedState(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	host.RegisterPluginID("channel/telegram")
	host.RegisterMetadata(pkgplugins.PluginMeta{
		ID:           "channel/telegram",
		Kind:         config.PluginKindChannel,
		Name:         "telegram",
		DisplayName:  "Telegram",
		AdminVisible: true,
		HasConfig:    true,
	})
	host.RegisterConfig(pkgplugins.ConfigRegistration{PluginID: "channel/telegram", DefaultConfig: func() map[string]any { return map[string]any{"token": ""} }})

	plugins, err := host.ListAdminVisiblePlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %#v", plugins)
	}
	if plugins[0].Persisted {
		t.Fatalf("expected non-persisted builtin entry: %#v", plugins[0])
	}
	if plugins[0].State.ID != "channel/telegram" || plugins[0].State.Enabled {
		t.Fatalf("unexpected default state: %#v", plugins[0].State)
	}
	if _, ok := plugins[0].State.Config["token"]; !ok {
		t.Fatalf("expected default config in discovery entry: %#v", plugins[0].State.Config)
	}
}

func TestChannelRuntimeServicesExtension(t *testing.T) {
	services := NewChannelRuntimeServices()
	host := New(&stubStore{plugins: map[string]config.Plugin{}}, WithChannelRuntimeServices(services))

	handler := &fakeChannelHandler{}
	notifications := &fakeNotificationRegistry{}
	services.Set(context.Background(), handler, notifications)

	resolved := host.Services().ChannelRuntime()
	if resolved == nil {
		t.Fatal("expected channel runtime services")
	}
	if resolved.ParentContext() == nil {
		t.Fatal("expected parent context")
	}
	if resolved.Handler() != handler {
		t.Fatalf("unexpected handler: %#v", resolved.Handler())
	}
	if resolved.Notifications() != notifications {
		t.Fatalf("unexpected notification registry: %#v", resolved.Notifications())
	}
}

func TestHostBackedManagedRuntimeRegistrationAddsMetadataAndSchema(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}})

	host.RegisterQQ(QQDeps{})
	host.RegisterFeishu(FeishuDeps{})
	host.RegisterWeixin(WeixinDeps{})
	host.RegisterReflect(ReflectDeps{})

	plugins, err := host.ListAdminVisiblePlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]pkgplugins.RegisteredPlugin{}
	for _, plugin := range plugins {
		seen[plugin.Meta.ID] = plugin
	}

	for _, pluginID := range []string{
		internalchannel.QQPluginID,
		internalchannel.FeishuPluginID,
		internalchannel.WeixinPluginID,
		internalreflect.PluginID,
	} {
		entry, ok := seen[pluginID]
		if !ok {
			t.Fatalf("missing admin-visible plugin %q in %#v", pluginID, seen)
		}
		if !entry.Meta.Managed || !entry.Meta.HasConfig || !entry.Meta.HasStatus {
			t.Fatalf("unexpected metadata for %q: %#v", pluginID, entry.Meta)
		}
		if len(host.ConfigSchema(pluginID)) == 0 {
			t.Fatalf("expected non-empty schema for %q", pluginID)
		}
	}
}

func TestLoadDefaultCatalogIncludesMCPMetadata(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	if err := host.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}

	metas := host.ListRegisteredPlugins()
	for _, meta := range metas {
		if meta.ID != pkgmcp.PluginID {
			continue
		}
		if !meta.Managed || !meta.AdminVisible || !meta.HasConfig || !meta.HasStatus {
			t.Fatalf("unexpected mcp metadata: %#v", meta)
		}
		if got := host.ConfigSchema(pkgmcp.PluginID); len(got) == 0 {
			t.Fatal("expected non-empty mcp schema")
		}
		return
	}
	t.Fatalf("missing metadata for %q", pkgmcp.PluginID)
}

type fakeChannelHandler struct{}

func (*fakeChannelHandler) HandleIncoming(context.Context, pkgchannel.IncomingMessage, string, string) (string, bool, *pkgchannel.ChatStream, error) {
	return "", false, nil, nil
}
func (*fakeChannelHandler) ListModels() []pkgchannel.ModelOption { return nil }
func (*fakeChannelHandler) SwitchModel(string, string) error     { return nil }
func (*fakeChannelHandler) ListAgents(context.Context, pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	return nil, "", nil
}
func (*fakeChannelHandler) SwitchAgent(context.Context, pkgchannel.IncomingMessage, string) error {
	return nil
}

type fakeNotificationRegistry struct{}

func (*fakeNotificationRegistry) Register(pkgchannel.Channel) {}
func (*fakeNotificationRegistry) Unregister(string)           {}
