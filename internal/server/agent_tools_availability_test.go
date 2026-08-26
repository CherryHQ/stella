package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/server"
)

func TestListAgentToolsExposesOnlyCurrentCoreCatalog(t *testing.T) {
	env := setupAdmin(t)
	_, sessionID := newNonAdmin(t, env, "image-tools-user")
	agentID := createAgentAsUser(t, env, sessionID, "image-tools-agent")
	legacyName := string([]byte{'v', 'l', 'l', 'm'})
	env.rebuild(t, func(deps *server.Deps) {
		// A reserved legacy name must not create a second catalog row.
		deps.BuiltinTools = []agent.BuiltinTool{{Tool: fakeManagedTool{name: legacyName}}}
	})

	rr := doRequestWithSession(t, env.srv, sessionID, http.MethodGet, "/api/agents/"+agentID+"/tools", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list tools status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	response := parseResponse(t, rr)
	var list types.AgentToolList
	if err := json.Unmarshal(response.Data, &list); err != nil {
		t.Fatalf("unmarshal tool list: %v", err)
	}

	if len(list.Tools) < 2 {
		t.Fatalf("tool list = %#v, want core catalog", list.Tools)
	}
	var core []types.AgentTool
	for _, tool := range list.Tools {
		if tool.Source == "core" {
			core = append(core, tool)
		}
		if tool.Name == legacyName {
			t.Fatalf("legacy reserved name must not appear in the model-facing catalog: %#v", list.Tools)
		}
	}
	if len(core) != 2 {
		t.Fatalf("core catalog rows = %#v, want exactly bash and view_image", core)
	}
	for i, want := range []string{"bash", "view_image"} {
		if core[i].Name != want || !core[i].Enabled {
			t.Fatalf("core[%d] = %#v, want enabled %q", i, core[i], want)
		}
	}
}
