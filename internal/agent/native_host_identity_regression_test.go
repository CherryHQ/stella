package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/toolmeta"
	"github.com/CherryHQ/stella/pkg/tools"
)

func TestNativeHostIdentityRequiresRegistryAndStaticToolName(t *testing.T) {
	const name = "host_registered_action"
	meta := toolmeta.NewRegistry(toolmeta.ActionTool{
		Name: name, PluginID: "system/scheduler", LocalName: name,
	})
	store := &invocationNativeStore{enabled: true}
	for _, test := range []struct {
		name   string
		policy *plugin.NativePolicy
		want   error
	}{
		{name: "missing policy", want: plugin.ErrNativePolicyUnavailable},
		{name: "unregistered owner", policy: plugin.NewNativePolicy(store, plugin.NativeRegistryMap{}), want: plugin.ErrUnknownNativeID},
		{name: "registered owner", policy: plugin.NewNativePolicy(store, plugin.NativeRegistryMap{"system/scheduler": true})},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, err := runnerToolIdentity(meta, test.policy, name)
			if !errors.Is(err, test.want) {
				t.Fatalf("identity error = %v, want %v", err, test.want)
			}
			if test.want == nil && identity != (ToolIdentity{CoreToolName: name}) {
				t.Fatalf("Native tool identity = %#v, must not reference an Agent Plugin", identity)
			}
		})
	}
}

func TestNativeHostStaticToolOverrideAppliesAfterBuild(t *testing.T) {
	const name = "host_registered_action"
	store := &invocationNativeStore{enabled: true}
	cfg := failClosedConfig(t)
	cfg.NativePolicy = plugin.NewNativePolicy(store, plugin.NativeRegistryMap{"system/scheduler": true})
	cfg.ToolMetaRegistry = toolmeta.NewRegistry(toolmeta.ActionTool{
		Name: name, PluginID: "system/scheduler", LocalName: name,
	})
	calls := 0
	cfg.PluginTools = func(context.Context, pkgplugins.ToolBuildContext) ([]tools.Tool, error) {
		return []tools.Tool{countingTool{name: name, calls: &calls}}, nil
	}
	var rows []ToolOverride
	cfg.ToolOverrideFetcher = func(context.Context, string, string) ([]ToolOverride, error) {
		return rows, nil
	}
	registry, _, _, err := buildToolRegistry(t.Context(), cfg, &fakeSession{alive: true}, nil, ai.Model{}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := registry.Execute(t.Context(), name, nil); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{ToolOverrideScopeSystem, ToolOverrideScopeSystemAgent, ToolOverrideScopeUser, ToolOverrideScopeUserAgent} {
		rows = []ToolOverride{{Identity: ToolIdentity{CoreToolName: name}, Scope: scope, Enabled: false}}
		if _, err := registry.Execute(t.Context(), name, nil); !errors.Is(err, authz.ErrForbidden) {
			t.Errorf("Native Host tool ignored %s static-name deny: %v", scope, err)
		}
	}
	if calls != 1 {
		t.Fatalf("inner calls = %d, want only the initial allowed call", calls)
	}
}
