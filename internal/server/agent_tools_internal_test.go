package server

import (
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/mcp"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
	"github.com/CherryHQ/stella/pkg/toolmeta"
)

func TestMCPToolNameRequiresPluginID(t *testing.T) {
	want, err := agentpackage.ExportedToolName("settings.server", "main", "list")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := mcpToolName(mcp.Registration{PluginID: "settings.server"}, mcp.CatalogTool{Name: "list"}); !ok || got != want {
		t.Fatalf("mcpToolName with plugin ID = %q, %v; want %q, true", got, ok, want)
	}
	if got, ok := mcpToolName(mcp.Registration{Name: "settings_server"}, mcp.CatalogTool{Name: "list"}); ok || got != "" {
		t.Fatalf("mcpToolName without plugin ID = %q, %v; want empty, false", got, ok)
	}
}

func TestMCPToolIdentityPreservesRemoteToolName(t *testing.T) {
	reg := mcp.Registration{PluginID: "settings.server"}
	tool := mcp.CatalogTool{Name: "list-items"}
	identity, ok := mcpToolIdentity(reg, tool)
	if !ok {
		t.Fatal("mcpToolIdentity rejected a valid registration")
	}
	if identity.PluginID != reg.PluginID || identity.LocalToolName != tool.Name {
		t.Fatalf("mcpToolIdentity = %+v, want plugin=%q local=%q", identity, reg.PluginID, tool.Name)
	}
	want, err := agentpackage.ExportedToolName(reg.PluginID, "main", tool.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := mcpToolName(reg, tool); !ok || got != want {
		t.Fatalf("mcpToolName = %q, %v; want %q, true", got, ok, want)
	}
}

func TestToolFamilyUsesRegistryBeforeStableFallbacks(t *testing.T) {
	s := &Server{toolMeta: toolmeta.NewRegistry(
		toolmeta.ActionTool{Name: "goal_list", Family: "goal", Action: "list"},
	)}

	cases := []struct {
		name   string
		source string
		want   string
	}{
		{name: "goal_list", source: agentToolSourceBuiltin, want: "goal"},
		// Plugins never inherit a builtin family, even if a duplicate name reaches
		// this helper before the runner's collision guard rejects registration.
		{name: "goal_list", source: agentToolSourcePlugin, want: agentToolFamilyPlugin},
		{name: "bash", source: agentToolSourceCore, want: agentToolFamilyCore},
		{name: "notify", source: agentToolSourceBuiltin, want: agentToolFamilyOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.toolFamily(tc.name, tc.source); got != tc.want {
				t.Errorf("toolFamily(%q, %q) = %q, want %q", tc.name, tc.source, got, tc.want)
			}
		})
	}
}

func TestToolInputSchema(t *testing.T) {
	if got := toolInputSchema(nil); got != nil {
		t.Errorf("nil schema should map to nil, got %v", got)
	}
	if got := toolInputSchema(map[string]any{}); got != nil {
		t.Errorf("empty schema should map to nil, got %v", got)
	}
	schema := map[string]any{"type": "object", "required": []any{"action"}}
	got := toolInputSchema(schema)
	if got == nil {
		t.Fatal("non-empty schema should be returned")
	}
	if (*got)["type"] != "object" {
		t.Errorf("schema content not preserved: %v", *got)
	}
}

func TestTrustedHostToolIdentityRequiresNativePolicy(t *testing.T) {
	meta := toolmeta.NewRegistry(toolmeta.ActionTool{
		Name: "host__owned", PluginID: "tool/host", LocalName: "host__owned",
	})
	if _, err := trustedToolIdentityWithPolicy(meta, nil, "host__owned"); !errors.Is(err, pluginpkg.ErrNativePolicyUnavailable) {
		t.Fatalf("missing native policy error = %v, want ErrNativePolicyUnavailable", err)
	}
}
