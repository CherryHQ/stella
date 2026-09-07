package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/CherryHQ/stella/cmd/stellad/store"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/platform/config"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/server"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/toolmeta"
)

func TestAgentToolsHostNativeOverrideUsesStaticDBIdentity(t *testing.T) {
	const (
		nativeID = "tool/server-host"
		toolName = "host_server_action"
	)
	env := setupAdmin(t)
	env.pluginHost.RegisterPluginID(nativeID)
	env.pluginHost.AddTool(pkgplugins.ToolSpec{PluginID: nativeID, Name: toolName})

	nativeStore := store.NewDBStore(env.db)
	if err := nativeStore.UpsertPlugin(t.Context(), configPlugin(nativeID)); err != nil {
		t.Fatalf("seed native plugin: %v", err)
	}
	nativePolicy := pluginpkg.NewNativePolicy(nativeStore, pluginpkg.NativeRegistryMap{nativeID: true})
	env.pluginHost.SetNativePolicy(nativePolicy)
	plugins := pluginpkg.NewService(env.db, env.deps.AgentAccess, pluginpkg.NewCatalog(),
		pluginpkg.BackendPolicy{}, func(_ context.Context, fn func() error) error { return fn() })
	env.rebuild(t, func(d *server.Deps) {
		d.NativePolicy = nativePolicy
		d.PluginService = plugins
		d.ToolMeta = toolmeta.NewRegistry(toolmeta.ActionTool{
			Name: toolName, PluginID: nativeID, LocalName: toolName,
		})
	})

	agentID := findStellaID(t, env)
	list := func() map[string]any {
		rr := doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/tools", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("list tools status = %d (body: %s)", rr.Code, rr.Body.String())
		}
		var body struct {
			Tools []map[string]any `json:"tools"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode tools: %v", err)
		}
		for _, item := range body.Tools {
			if item["name"] == toolName {
				return item
			}
		}
		t.Fatalf("host tool %q missing from list: %#v", toolName, body.Tools)
		return nil
	}

	item := list()
	if item["source"] != "plugin" || item["control"] != "override" {
		t.Fatalf("host tool catalog item = %#v, want plugin override", item)
	}
	if enabled, ok := item["enabled"].(bool); !ok || !enabled {
		t.Fatalf("host tool enabled = %#v, want true", item["enabled"])
	}

	rr := doRequest(t, env, http.MethodPatch, "/api/agents/"+agentID+"/tools/"+toolName,
		map[string]any{"enabled": false, "scope": agent.ToolOverrideScopeUserAgent})
	if rr.Code != http.StatusOK {
		t.Fatalf("disable host tool status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	var toolNameDB, pluginIDDB, localNameDB *string
	if err := env.db.QueryRow(t.Context(), `
		SELECT tool_name, plugin_id, local_tool_name
		FROM tool_override
		WHERE scope = $1 AND agent_id = $2
	`, agent.ToolOverrideScopeUserAgent, agentID).Scan(&toolNameDB, &pluginIDDB, &localNameDB); err != nil {
		t.Fatalf("read host override: %v", err)
	}
	if toolNameDB == nil || *toolNameDB != toolName || pluginIDDB != nil || localNameDB != nil {
		t.Fatalf("host override identity = %v/%v/%v, want static core name", toolNameDB, pluginIDDB, localNameDB)
	}
	item = list()
	if enabled, ok := item["enabled"].(bool); !ok || enabled {
		t.Fatalf("disabled host tool = %#v, want false", item["enabled"])
	}

	rr = doRequest(t, env, http.MethodPatch, "/api/agents/"+agentID+"/tools/"+toolName,
		map[string]any{"scope": agent.ToolOverrideScopeUserAgent})
	if rr.Code != http.StatusOK {
		t.Fatalf("clear host tool status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	var count int
	if err := env.db.QueryRow(t.Context(), `
		SELECT count(*) FROM tool_override
		WHERE tool_name = $1 AND scope = $2 AND agent_id = $3
	`, toolName, agent.ToolOverrideScopeUserAgent, agentID).Scan(&count); err != nil {
		t.Fatalf("count host override: %v", err)
	}
	if count != 0 {
		t.Fatalf("host override rows after clear = %d, want 0", count)
	}
}

func configPlugin(id string) config.Plugin {
	return config.Plugin{ID: id, Kind: "tool", Name: "server-host", Enabled: true, Config: map[string]any{}}
}
