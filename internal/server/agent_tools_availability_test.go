package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/server"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

func TestListAgentToolsIncludesRuntimeBuiltBuiltin(t *testing.T) {
	env := setupAdmin(t)
	_, sessionID := newNonAdmin(t, env, "runtime-tool-user")
	agentID := createAgentAsUser(t, env, sessionID, "runtime-tool-agent")
	env.rebuild(t, func(deps *server.Deps) {
		deps.BuiltinTools = []agent.BuiltinTool{{
			Build: func(pkgplugins.ToolBuildContext) (pkgtools.Tool, error) { return fakeManagedTool{name: "recally"}, nil },
			Spec:  fakeManagedTool{name: "recally"}.Definition(),
		}}
	})

	rr := doRequestWithSession(t, env.srv, sessionID, http.MethodGet, "/api/agents/"+agentID+"/tools", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list tools status = %d, body: %s", rr.Code, rr.Body.String())
	}
	response := parseResponse(t, rr)
	var list types.AgentToolList
	if err := json.Unmarshal(response.Data, &list); err != nil {
		t.Fatal(err)
	}
	for _, tool := range list.Tools {
		if tool.Name == "recally" && tool.Source == "builtin" {
			if tool.Control != "override" || tool.Enabled == nil || !*tool.Enabled || tool.Origin == nil {
				t.Fatalf("recally catalog metadata = %#v, want runnable override", tool)
			}
			return
		}
	}
	t.Fatalf("runtime-built recally missing from tool list: %#v", list.Tools)
}

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
		if core[i].Name != want || core[i].Control != "system" || core[i].Enabled != nil || core[i].Origin != nil || core[i].PolicyReason == nil || *core[i].PolicyReason != "core_sandbox" {
			t.Fatalf("core[%d] = %#v, want system-managed %q", i, core[i], want)
		}
	}
}

// An availability probe that cannot answer must not leave the catalog showing
// the tool's last known state: the operator would toggle a row the runner will
// refuse to build. Fail the request instead.
func TestListAgentToolsFailsWhenAvailabilityIsUnknown(t *testing.T) {
	env := setupAdmin(t)
	_, sessionID := newNonAdmin(t, env, "availability-error-user")
	agentID := createAgentAsUser(t, env, sessionID, "availability-error-agent")
	env.rebuild(t, func(deps *server.Deps) {
		deps.BuiltinTools = []agent.BuiltinTool{{
			Tool: fakeManagedTool{name: "email"},
			Available: func(context.Context, agent.RunnerParams) (bool, error) {
				return false, errors.New("vault unreachable")
			},
		}}
	})

	rr := doRequestWithSession(t, env.srv, sessionID, http.MethodGet, "/api/agents/"+agentID+"/tools", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("list tools status = %d, want %d (body: %s)", rr.Code, http.StatusInternalServerError, rr.Body.String())
	}
	if body := rr.Body.String(); strings.Contains(body, "\"email\"") {
		t.Fatalf("stale tool state must not be served on a failed probe: %s", body)
	}
}
