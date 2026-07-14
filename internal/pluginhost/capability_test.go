package pluginhost

// Issue #712 Item 2: the plugin-scoped Platform exposes ONLY the capabilities a
// plugin declared in PluginInfo.RequiredCapabilities. The host grants only those,
// validates each declared capability is backed by an injected service, and
// refuses to start a managed runtime whose required capability is unbacked.

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// TestPlatformExposesOnlyDeclaredCapabilities proves both directions: a declared
// capability yields a non-nil scoped service, and an undeclared one fails closed
// to nil even though the host service is bound (ambient removal proof).
func TestPlatformExposesOnlyDeclaredCapabilities(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}},
		WithStateStore(&fakeStateStoreBackend{}),
		WithSchedulerService(&fakeSchedulerBackend{}),
		WithNotificationService(&fakeNotificationService{}),
		WithAuthService(&fakeAuthService{}),
		WithChannelRuntimeServices(NewChannelRuntimeServices()),
	)
	host.RegisterPluginID("tool/partial")
	host.SetInfo(pkgplugins.PluginInfo{
		ID:          "tool/partial",
		Kind:        "tool",
		Name:        "partial",
		DisplayName: "Partial",
		RequiredCapabilities: []pkgplugins.Capability{
			pkgplugins.CapabilityLogger,
			pkgplugins.CapabilityStateStore,
			pkgplugins.CapabilityChannelPlatform,
		},
	})

	p := host.platform("tool/partial")

	// Declared capabilities are exposed.
	if p.Logger() == nil {
		t.Fatal("declared Logger capability returned nil")
	}
	if p.StateStore() == nil {
		t.Fatal("declared StateStore capability returned nil")
	}
	if p.ChannelPlatform() == nil {
		t.Fatal("declared ChannelPlatform capability returned nil")
	}

	// Undeclared capabilities fail closed to nil despite being backed.
	if p.Scheduler() != nil {
		t.Fatal("undeclared Scheduler capability must be nil")
	}
	if p.Notifier() != nil {
		t.Fatal("undeclared Notifier capability must be nil")
	}
	if p.Auth() != nil {
		t.Fatal("undeclared Auth capability must be nil")
	}
	if p.ConfigStore() != nil {
		t.Fatal("undeclared ConfigStore capability must be nil")
	}
	if p.RuntimeLookup() != nil {
		t.Fatal("undeclared RuntimeLookup capability must be nil")
	}
	if p.SkillStore() != nil {
		t.Fatal("undeclared SkillStore capability must be nil")
	}
}

// TestPlatformWithoutMetadataFailsClosed proves a plugin that never declared
// metadata (or capabilities) reaches no host port.
func TestPlatformWithoutMetadataFailsClosed(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}},
		WithStateStore(&fakeStateStoreBackend{}),
		WithChannelRuntimeServices(NewChannelRuntimeServices()),
	)
	p := host.platform("tool/undeclared")
	if p.Logger() != nil || p.StateStore() != nil || p.ChannelPlatform() != nil ||
		p.RuntimeLookup() != nil || p.ConfigStore() != nil {
		t.Fatal("plugin with no declared capabilities must reach no Platform port")
	}
}

// TestValidateRejectsUnbackedRequiredCapability proves Seal/ValidateRegistrations
// fail closed when a plugin declares a capability the host cannot serve.
func TestValidateRejectsUnbackedRequiredCapability(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}}) // no channel services bound
	host.RegisterPluginID("tool/needschan")
	host.SetInfo(pkgplugins.PluginInfo{
		ID:                   "tool/needschan",
		Kind:                 "tool",
		Name:                 "needschan",
		DisplayName:          "NeedsChan",
		RequiredCapabilities: []pkgplugins.Capability{pkgplugins.CapabilityChannelPlatform},
	})

	err := host.ValidateRegistrations()
	if err == nil || !strings.Contains(err.Error(), string(pkgplugins.CapabilityChannelPlatform)) {
		t.Fatalf("ValidateRegistrations error = %v, want unbacked channel_platform", err)
	}
	if err := host.Seal(); err == nil {
		t.Fatal("Seal must refuse a plugin declaring an unbacked capability")
	}
}

// TestValidateAcceptsBackedRequiredCapability is the positive counterpart: once
// the backing service is bound, validation passes.
func TestValidateAcceptsBackedRequiredCapability(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}},
		WithChannelRuntimeServices(NewChannelRuntimeServices()))
	host.RegisterPluginID("tool/needschan")
	host.SetInfo(pkgplugins.PluginInfo{
		ID:                   "tool/needschan",
		Kind:                 "tool",
		Name:                 "needschan",
		DisplayName:          "NeedsChan",
		RequiredCapabilities: []pkgplugins.Capability{pkgplugins.CapabilityChannelPlatform},
	})
	if err := host.ValidateRegistrations(); err != nil {
		t.Fatalf("ValidateRegistrations with backed capability: %v", err)
	}
}

// TestStartRefusedWhenRequiredCapabilityUnbacked proves the runtime start path
// refuses (fail-closed) and never builds the runtime when a required capability
// is unbacked.
func TestStartRefusedWhenRequiredCapabilityUnbacked(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{"tool/needschan": {ID: "tool/needschan", Enabled: true}}}
	host := New(store) // no channel services bound
	host.RegisterPluginID("tool/needschan")
	host.SetInfo(pkgplugins.PluginInfo{
		ID:                   "tool/needschan",
		Kind:                 "tool",
		Name:                 "needschan",
		DisplayName:          "NeedsChan",
		RequiredCapabilities: []pkgplugins.Capability{pkgplugins.CapabilityChannelPlatform},
	})
	built := false
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "tool/needschan", Name: "main", Build: func(pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		built = true
		return runtimeStub{apply: func(context.Context, pkgplugins.PluginState) error { return nil }}, nil
	}})

	err := host.ApplyPlugin(context.Background(), "tool/needschan")
	if err == nil || !strings.Contains(err.Error(), string(pkgplugins.CapabilityChannelPlatform)) {
		t.Fatalf("ApplyPlugin error = %v, want refusal for unbacked channel_platform", err)
	}
	if built {
		t.Fatal("runtime must not be built when a required capability is unbacked")
	}
}

// TestStartAllowedWhenRequiredCapabilityBacked is the positive counterpart:
// binding the backing service lets the runtime start.
func TestStartAllowedWhenRequiredCapabilityBacked(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{"tool/needschan": {ID: "tool/needschan", Enabled: true}}}
	host := New(store, WithChannelRuntimeServices(NewChannelRuntimeServices()))
	host.RegisterPluginID("tool/needschan")
	host.SetInfo(pkgplugins.PluginInfo{
		ID:                   "tool/needschan",
		Kind:                 "tool",
		Name:                 "needschan",
		DisplayName:          "NeedsChan",
		RequiredCapabilities: []pkgplugins.Capability{pkgplugins.CapabilityChannelPlatform},
	})
	built := false
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "tool/needschan", Name: "main", Build: func(pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		built = true
		return runtimeStub{apply: func(context.Context, pkgplugins.PluginState) error { return nil }}, nil
	}})

	if err := host.ApplyPlugin(context.Background(), "tool/needschan"); err != nil {
		t.Fatalf("ApplyPlugin with backed capability: %v", err)
	}
	if !built {
		t.Fatal("runtime should build once its required capability is backed")
	}
}

// TestManifestPluginsReceiveNoPlatformCapabilities proves a user-editable
// manifest cannot grant its plugin host ports after static composition seals.
func TestManifestPluginsReceiveNoPlatformCapabilities(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}},
		WithChannelRuntimeServices(NewChannelRuntimeServices()))
	host.RegisterManifestPlugins(&manifestplugins.Manifest{
		Plugins: []manifestplugins.ManifestPlugin{{
			ID: "tool/manifest", Kind: "tool", Name: "manifest", Enabled: true, Prompt: "Use this tool.",
		}},
	})
	if host.platform("tool/manifest").ChannelPlatform() != nil {
		t.Fatal("manifest plugin must not receive a Platform capability")
	}
	for _, meta := range host.ListRegisteredPlugins() {
		if meta.ID == "tool/manifest" && len(meta.RequiredCapabilities) != 0 {
			t.Fatalf("manifest mutated RequiredCapabilities: %#v", meta)
		}
	}
}
