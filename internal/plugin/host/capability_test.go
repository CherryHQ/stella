package host

// Issue #712 Item 2: the plugin-scoped Platform exposes ONLY the capabilities a
// plugin declared in PluginInfo.RequiredCapabilities. The host grants only those,
// validates each declared capability is backed by an injected service, and
// refuses to start a managed runtime whose required capability is unbacked.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	_ "github.com/CherryHQ/stella/plugins/channels/dingtalk"
	_ "github.com/CherryHQ/stella/plugins/channels/discord"
	_ "github.com/CherryHQ/stella/plugins/channels/feishu"
	_ "github.com/CherryHQ/stella/plugins/channels/telegram"
)

type recordingAccountEnroller struct {
	got       pkgchannel.EnrollmentRequest
	namespace string
}

func (r *recordingAccountEnroller) EnrollAccount(_ context.Context, namespace string, req pkgchannel.EnrollmentRequest) error {
	r.got = req
	r.namespace = namespace
	return nil
}

func TestAccountEnrollmentNamespaceIsHostBound(t *testing.T) {
	recorder := &recordingAccountEnroller{}
	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	host.RegisterPluginID("channel/fake")
	host.SetInfo(pkgplugins.PluginInfo{
		ID:                   "channel/fake",
		Kind:                 "channel",
		Name:                 "fake",
		DisplayName:          "Fake",
		RequiredCapabilities: []pkgplugins.Capability{pkgplugins.CapabilityAccountEnrollment},
	})
	host.AddChannel(pkgplugins.ChannelSpec{PluginID: "channel/fake", Name: "fake-channel"})

	if host.platform("channel/fake").AccountEnrollment() != nil {
		t.Fatal("unbacked enrollment must be unavailable")
	}
	if err := host.Seal(); err == nil {
		t.Fatal("Seal accepted missing enrollment backing")
	}
	host.SetAccountEnrollment(recorder)
	if err := host.Seal(); err != nil {
		t.Fatal(err)
	}
	enroller := host.platform("channel/fake").AccountEnrollment()
	if err := enroller.EnrollAccount(context.Background(), pkgchannel.EnrollmentRequest{Subject: "subject"}); err != nil {
		t.Fatal(err)
	}
	if recorder.namespace != "fake-channel" {
		t.Fatalf("namespace = %q, want host-bound fake-channel", recorder.namespace)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("sealed host allowed enrollment replacement")
		}
	}()
	host.SetAccountEnrollment(recorder)
}

func TestAccountEnrollmentRequiresExactlyOnePort(t *testing.T) {
	for _, ports := range [][]string{nil, {"first", "second"}} {
		t.Run(fmt.Sprintf("%d_ports", len(ports)), func(t *testing.T) {
			host := New(&stubStore{plugins: map[string]config.Plugin{}}, WithChannelRuntimeServices(NewChannelRuntimeServices()))
			host.SetAccountEnrollment(fakeAccountEnroller{})
			host.RegisterPluginID("channel/fake")
			host.SetInfo(pkgplugins.PluginInfo{
				ID: "channel/fake", Kind: "channel", Name: "fake", DisplayName: "Fake",
				RequiredCapabilities: []pkgplugins.Capability{pkgplugins.CapabilityChannelPlatform, pkgplugins.CapabilityAccountEnrollment},
			})
			for _, name := range ports {
				host.AddChannel(pkgplugins.ChannelSpec{PluginID: "channel/fake", Name: name})
			}
			if host.platform("channel/fake").AccountEnrollment() != nil {
				t.Fatal("ambiguous or missing channel must not grant enrollment")
			}
			if err := host.ValidateRegistrations(); err == nil || !strings.Contains(err.Error(), "exactly one") {
				t.Fatalf("ValidateRegistrations error = %v, want exactly-one-port error", err)
			}
		})
	}
}

func TestAccountEnrollmentRequiresDeclaredCapability(t *testing.T) {
	services := NewChannelRuntimeServices()
	host := New(&stubStore{plugins: map[string]config.Plugin{}}, WithChannelRuntimeServices(services))
	host.SetAccountEnrollment(fakeAccountEnroller{})
	host.RegisterPluginID("channel/fake")
	host.SetInfo(pkgplugins.PluginInfo{
		ID: "channel/fake", Kind: "channel", Name: "fake", DisplayName: "Fake",
		RequiredCapabilities: []pkgplugins.Capability{pkgplugins.CapabilityChannelPlatform},
	})
	host.AddChannel(pkgplugins.ChannelSpec{PluginID: "channel/fake", Name: "fake-channel"})
	platform := host.platform("channel/fake").ChannelPlatform()
	if platform == nil {
		t.Fatal("declared channel platform must be available")
	}
	if host.platform("channel/fake").AccountEnrollment() != nil {
		t.Fatal("account enrollment must remain unavailable when capability is undeclared")
	}
}

func TestGuestPolicyResolverUsesRegisteredPluginDecoder(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}}, WithChannelRuntimeServices(NewChannelRuntimeServices()))
	if err := host.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}
	for channelType, raw := range map[string]string{
		"discord":  `{"token":"token","allow_dm":true,"allow_unlinked_dm":true}`,
		"telegram": `{"token":"token","allow_dm":true,"allow_unlinked_dm":true}`,
		"feishu":   `{"app_id":"id","app_secret":"secret","allow_dm":true,"allow_unlinked_dm":true}`,
		"dingtalk": `{"client_id":"id","client_secret":"secret","allow_dm":true,"allow_unlinked_dm":true}`,
	} {
		policy, err := host.GuestPolicyResolver(channelType, raw)
		if err != nil {
			t.Fatalf("GuestPolicyResolver(%q): %v", channelType, err)
		}
		if !policy.AllowDM || !policy.AllowUnlinkedDM {
			t.Fatalf("policy for %q = %+v, want enabled guest DMs", channelType, policy)
		}
		if _, err := host.GuestPolicyResolver(channelType, `{`); err == nil {
			t.Fatalf("GuestPolicyResolver(%q) accepted malformed config", channelType)
		}
	}
	if _, err := host.GuestPolicyResolver("qq", `{"app_id":"id","app_secret":"secret"}`); err == nil {
		t.Fatal("channel without registered guest decoder must fail closed")
	}
}

func TestGuestPolicyResolverPreservesPluginDecoderSemantics(t *testing.T) {
	host := New(nil)
	if err := host.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}
	for _, channelType := range []string{"discord", "telegram", "feishu", "dingtalk"} {
		t.Run(channelType, func(t *testing.T) {
			policy, err := host.GuestPolicyResolver(channelType, `{"allow_dm":true,"allow_unlinked_dm":true}`)
			if err != nil || !policy.AllowDM || !policy.AllowUnlinkedDM {
				t.Fatalf("credential-free opt-in policy = %+v, err=%v", policy, err)
			}
			policy, err = host.GuestPolicyResolver(channelType, `null`)
			if err != nil || !policy.AllowDM || policy.AllowUnlinkedDM || policy.GuestMessageLimitPerMinute != pkgchannel.DefaultGuestMessageLimitPerMinute {
				t.Fatalf("null/default policy = %+v, err=%v", policy, err)
			}
			if _, err := host.GuestPolicyResolver(channelType, `{"allow_dm":"yes"}`); err == nil {
				t.Fatal("known field type error accepted")
			}
		})
	}
	if _, err := host.GuestPolicyResolver("telegram", `{"allowed_chat_ids":[" "]}`); err == nil {
		t.Fatal("Telegram blank allowlist entry accepted")
	}
}

func TestGuestPolicyResolverAcceptsNonBuiltinChannelRegistration(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	host.RegisterPluginID("channel/fake")
	host.SetInfo(pkgplugins.PluginInfo{ID: "channel/fake", Kind: "channel", Name: "fake", DisplayName: "Fake"})
	host.AddChannel(pkgplugins.ChannelSpec{
		PluginID: "channel/fake",
		Name:     "fake",
		GuestPolicy: func(raw string) (pkgchannel.GuestConfig, error) {
			if raw != "enabled" {
				return pkgchannel.GuestConfig{}, errors.New("invalid fake config")
			}
			return pkgchannel.GuestConfig{AllowDM: true, AllowUnlinkedDM: true}, nil
		},
	})
	policy, err := host.GuestPolicyResolver("fake", "enabled")
	if err != nil || !policy.AllowDM || !policy.AllowUnlinkedDM {
		t.Fatalf("fake guest policy = %+v, err=%v", policy, err)
	}
}

// TestPlatformExposesOnlyDeclaredCapabilities proves both directions: a declared
// capability yields a non-nil scoped service, and an undeclared one fails closed
// to nil even though the host service is bound (ambient removal proof).
func TestPlatformExposesOnlyDeclaredCapabilities(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}},
		WithStateStore(&fakeStateStoreBackend{}),
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

// TestValidateRejectsUnbackedRequiredCapability proves Seal fails closed when
// a plugin declares a capability the host cannot serve.
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

	if err := host.Seal(); err == nil || !strings.Contains(err.Error(), string(pkgplugins.CapabilityChannelPlatform)) {
		t.Fatalf("Seal error = %v, want unbacked channel_platform", err)
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
	host.RegisterManifestPlugins(&manifest.Manifest{
		Plugins: []manifest.ManifestPlugin{{
			ID: "tool/manifest", Kind: "tool", Enabled: true,
			ManifestPluginDefinition: manifest.ManifestPluginDefinition{Name: "manifest", Prompt: "Use this tool."},
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
