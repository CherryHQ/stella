package host

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	feishuplugin "github.com/CherryHQ/stella/plugins/channels/feishu"
	qqplugin "github.com/CherryHQ/stella/plugins/channels/qq"
	weixinplugin "github.com/CherryHQ/stella/plugins/channels/weixin"
)

func TestRegisterMetadataPanicsOnDuplicate(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	host.RegisterPluginID("channel/telegram")
	host.SetInfo(pkgplugins.PluginInfo{
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

	host.SetInfo(pkgplugins.PluginInfo{
		ID:          "channel/telegram",
		Kind:        config.PluginKindChannel,
		Name:        "telegram",
		DisplayName: "Telegram",
	})
}

func TestLoadCatalogFailsOnIncompleteManagedMetadata(t *testing.T) {
	catalog := pkgplugins.NewCatalog()
	catalog.Register("channel/telegram", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
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

func TestDiscoveryReportsRegistrations(t *testing.T) {
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
	host.SetInfo(pkgplugins.PluginInfo{
		ID:           "channel/telegram",
		Kind:         config.PluginKindChannel,
		Name:         "telegram",
		DisplayName:  "Telegram",
		Managed:      true,
		AdminVisible: true,
		HasConfig:    true,
		HasStatus:    true,
		Capabilities: []string{
			pkgplugins.CapabilityChannel,
			pkgplugins.CapabilityRuntime,
			pkgplugins.CapabilityConfig,
			pkgplugins.CapabilityStatus,
		},
	})
	host.AddAdmin(pkgplugins.AdminSpec{PluginID: "channel/telegram", DefaultConfig: func() map[string]any { return map[string]any{"token": ""} }})
	host.AddChannel(pkgplugins.ChannelSpec{PluginID: "channel/telegram", Name: "telegram"})
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "channel/telegram", Name: "bot", Build: func(pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		return runtimeStub{apply: func(context.Context, pkgplugins.PluginState) error { return nil }}, nil
	}})
	host.AddAdmin(pkgplugins.AdminSpec{PluginID: "channel/telegram", Status: func(context.Context, pkgplugins.AdminContext) (any, error) { return map[string]any{}, nil }})
	if err := host.ValidateRegistrations(); err != nil {
		t.Fatalf("ValidateRegistrations: %v", err)
	}

	if !host.HasRuntime("channel/telegram") || !host.HasConfig("channel/telegram") || !host.HasStatus("channel/telegram") {
		t.Fatal("expected runtime/config/status registrations")
	}
}

func TestChannelRuntimeServicesExtension(t *testing.T) {
	services := NewChannelRuntimeServices()
	host := New(&stubStore{plugins: map[string]config.Plugin{}}, WithChannelRuntimeServices(services))

	handler := &fakeChannelHandler{}
	notifications := &fakeNotificationRegistry{}
	services.Set(context.Background(), handler, notifications, nil)

	resolved := host.ChannelRuntime()
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

type fakeAccountEnroller struct{}

func (fakeAccountEnroller) EnrollAccount(context.Context, string, pkgchannel.EnrollmentRequest) error {
	return nil
}

func TestNotificationServiceExtension(t *testing.T) {
	notifications := &fakeNotificationService{}
	host := New(&stubStore{plugins: map[string]config.Plugin{}}, WithNotificationService(notifications))

	resolved := host.Notifications()
	if resolved == nil {
		t.Fatal("expected notification service")
	}
	if resolved != notifications {
		t.Fatalf("unexpected notification service: %#v", resolved)
	}
}

func TestStateStoreExtension(t *testing.T) {
	stateStore := &fakeStateStoreBackend{}
	host := New(&stubStore{plugins: map[string]config.Plugin{}}, WithStateStore(stateStore))

	resolved := host.StateStore()
	if resolved == nil {
		t.Fatal("expected state store")
	}
	if resolved != stateStore {
		t.Fatalf("unexpected state store: %#v", resolved)
	}
}

func TestAuthServiceExtension(t *testing.T) {
	authService := &fakeAuthService{}
	host := New(&stubStore{plugins: map[string]config.Plugin{}}, WithAuthService(authService))

	resolved := host.Auth()
	if resolved == nil {
		t.Fatal("expected auth service")
	}
	if resolved != authService {
		t.Fatalf("unexpected auth service: %#v", resolved)
	}
}

func TestHostBackedManagedRuntimeRegistrationAddsMetadataAndSchema(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}})

	qqPlugin, ok := pkgplugins.Get(qqplugin.PluginID)
	if !ok {
		t.Fatalf("missing plugin %q", qqplugin.PluginID)
	}
	host.RegisterPluginID(qqplugin.PluginID)
	qqPlugin.Register(host)
	feishuPlugin, ok := pkgplugins.Get(feishuplugin.PluginID)
	if !ok {
		t.Fatalf("missing plugin %q", feishuplugin.PluginID)
	}
	host.RegisterPluginID(feishuplugin.PluginID)
	feishuPlugin.Register(host)
	weixinPlugin, ok := pkgplugins.Get(weixinplugin.PluginID)
	if !ok {
		t.Fatalf("missing plugin %q", weixinplugin.PluginID)
	}
	host.RegisterPluginID(weixinplugin.PluginID)
	weixinPlugin.Register(host)

	if err := host.ValidateRegistrations(); err != nil {
		t.Fatal(err)
	}

	for _, pluginID := range []string{
		qqplugin.PluginID,
		feishuplugin.PluginID,
		weixinplugin.PluginID,
	} {
		if !host.HasRuntime(pluginID) || !host.HasStatus(pluginID) {
			t.Fatalf("missing runtime/status registration for %q", pluginID)
		}
		if len(host.ConfigSchema(pluginID)) == 0 {
			t.Fatalf("expected non-empty schema for %q", pluginID)
		}
	}
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

type fakeNotificationService struct{}

func (*fakeNotificationService) Notify(context.Context, pkgchannel.Notification) error {
	return nil
}

func (*fakeNotificationService) NotifyUser(context.Context, string, pkgchannel.Notification) error {
	return nil
}

type fakeStateStoreBackend struct{}

func (*fakeStateStoreBackend) Get(context.Context, string, pkgplugins.StateScope, string) (map[string]any, bool, error) {
	return nil, false, nil
}

func (*fakeStateStoreBackend) Set(context.Context, string, pkgplugins.StateScope, string, map[string]any) error {
	return nil
}

func (*fakeStateStoreBackend) Delete(context.Context, string, pkgplugins.StateScope, string) error {
	return nil
}

type fakeAuthService struct{}

func (*fakeAuthService) GetUser(context.Context, string) (pkgplugins.UserInfo, error) {
	return pkgplugins.UserInfo{}, nil
}

func (*fakeAuthService) ListUserIdentities(context.Context, string) ([]pkgplugins.LinkedIdentity, error) {
	return nil, nil
}

func (*fakeAuthService) GetIdentityByPlatform(context.Context, string, string) (pkgplugins.LinkedIdentity, error) {
	return pkgplugins.LinkedIdentity{}, nil
}
