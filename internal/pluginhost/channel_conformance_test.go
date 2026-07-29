package pluginhost

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	_ "github.com/CherryHQ/stella/plugins/builtin"
)

// TestBuiltinChannelConformance is the shared contract for every channel
// shipped in the builtin catalog. Adding a channel without adding it here fails
// the exact registration/configuration boundary used by the runtime.
func TestBuiltinChannelConformance(t *testing.T) {
	tests := []struct {
		pluginID string
		name     string
		managed  bool
		valid    map[string]any
		secrets  []string
	}{
		{
			pluginID: "channel/feishu",
			name:     "feishu",
			managed:  true,
			valid:    map[string]any{"app_id": "feishu-app", "app_secret": "feishu-secret"},
			secrets:  []string{"feishu-secret"},
		},
		{
			pluginID: "channel/qq",
			name:     "qq",
			managed:  true,
			valid:    map[string]any{"app_id": "qq-app", "app_secret": "qq-secret"},
			secrets:  []string{"qq-secret"},
		},
		{
			pluginID: "channel/telegram",
			name:     "telegram",
			managed:  true,
			valid:    map[string]any{"token": "telegram-secret"},
			secrets:  []string{"telegram-secret"},
		},
		{
			pluginID: "channel/webhook",
			name:     "webhook",
			managed:  false,
			valid:    map[string]any{"session_mode": "persistent", "wait_timeout_seconds": 30},
		},
		{
			pluginID: "channel/weixin",
			name:     "weixin",
			managed:  true,
			valid:    map[string]any{"bot_token": "weixin-secret"},
			secrets:  []string{"weixin-secret"},
		},
	}

	wantIDs := make([]string, 0, len(tests))
	for _, tt := range tests {
		wantIDs = append(wantIDs, tt.pluginID)
	}
	sort.Strings(wantIDs)

	// Derive the actual set from the same process-wide catalog used by stellad.
	// This makes a newly linked builtin channel fail until it joins the shared
	// conformance table instead of silently escaping coverage.
	var actualIDs []string
	for _, id := range pkgplugins.Names() {
		if strings.HasPrefix(id, "channel/") {
			actualIDs = append(actualIDs, id)
		}
	}
	if !slices.Equal(actualIDs, wantIDs) {
		t.Fatalf("builtin channel IDs = %v, conformance table = %v", actualIDs, wantIDs)
	}

	catalog := pkgplugins.NewCatalog()
	for _, id := range actualIDs {
		plugin, ok := pkgplugins.Get(id)
		if !ok {
			t.Fatalf("builtin catalog listed but could not resolve %q", id)
		}
		catalog.Register(id, plugin)
	}
	host := New(
		&stubStore{plugins: map[string]config.Plugin{}},
		WithChannelRuntimeServices(NewChannelRuntimeServices()),
	)
	if err := host.LoadCatalog(catalog); err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if err := host.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	gotIDs := host.PluginsByKind(config.PluginKindChannel)
	sort.Strings(gotIDs)
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("registered channel IDs = %v, want %v", gotIDs, wantIDs)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := host.metadataRegs[tt.pluginID]
			if !ok {
				t.Fatalf("metadata missing for %q", tt.pluginID)
			}
			if info.Name != tt.name || info.Managed != tt.managed || !info.AdminVisible {
				t.Fatalf("metadata = %+v, want name=%q managed=%t admin-visible", info, tt.name, tt.managed)
			}

			channelReg, ok := host.channelRegs[tt.name]
			if !ok || channelReg.PluginID != tt.pluginID || channelReg.Configured == nil {
				t.Fatalf("channel registration = %+v, present=%t", channelReg, ok)
			}
			if channelReg.Build != nil {
				t.Fatal("builtin channels must use the host-managed runtime or HTTP ingress, not a second Channel.Build path")
			}
			if !channelReg.Configured(tt.valid) {
				t.Fatalf("valid config rejected: %#v", tt.valid)
			}
			if tt.managed && channelReg.Configured(map[string]any{}) {
				t.Fatal("empty managed-channel config must not be runnable")
			}

			admin, ok := host.configRegs[tt.pluginID]
			if !ok || admin.DefaultConfig == nil || admin.Validate == nil || admin.Redact == nil || len(admin.Schema) == 0 {
				t.Fatalf("incomplete admin contract: %+v, present=%t", admin, ok)
			}
			if err := admin.Validate(tt.valid); err != nil {
				t.Fatalf("Validate(valid): %v", err)
			}
			before, err := json.Marshal(tt.valid)
			if err != nil {
				t.Fatal(err)
			}
			redacted, err := json.Marshal(admin.Redacted(tt.valid))
			if err != nil {
				t.Fatal(err)
			}
			after, err := json.Marshal(tt.valid)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("redaction mutated caller config: before=%s after=%s", before, after)
			}
			for _, secret := range tt.secrets {
				if strings.Contains(string(redacted), secret) {
					t.Fatalf("redacted config leaked %q: %s", secret, redacted)
				}
			}

			hasConfig, hasStatus, capabilities := host.derivedTraits(tt.pluginID)
			if !hasConfig || !slices.Contains(capabilities, pkgplugins.CapabilityChannel) {
				t.Fatalf("derived traits missing channel/config: status=%t capabilities=%v", hasStatus, capabilities)
			}
			if tt.managed {
				if !host.HasRuntime(tt.pluginID) || !hasStatus || host.statusRegs[tt.pluginID].Status == nil {
					t.Fatal("managed channel must register one runtime and status contract")
				}
			} else if host.HasRuntime(tt.pluginID) || hasStatus {
				t.Fatal("ingress-only channel must not register a long-running runtime or status")
			}
		})
	}
}
