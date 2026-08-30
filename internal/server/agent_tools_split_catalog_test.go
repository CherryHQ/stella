package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/agent/toolmeta"
	"github.com/CherryHQ/stella/internal/connections"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/server"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	skillstool "github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/internal/vault"
	workflowpkg "github.com/CherryHQ/stella/internal/workflow"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// splitCatalog is the model-facing name of every split builtin, in the order
// the families register. It is a golden list on purpose: the catalog is what
// the Web UI lists, what an operator toggles, and what a tool_override row is
// keyed by, so a name changing here is a migration, not a refactor
// (rules/agent-tools.md §10).
var splitCatalog = []string{
	"goal_cancel", "goal_create", "goal_get", "goal_list",
	"scheduler_job_create", "scheduler_job_delete", "scheduler_job_get",
	"scheduler_job_list", "scheduler_job_pause", "scheduler_job_resume",
	"scheduler_job_update",
	"workflow_get", "workflow_list", "workflow_run", "workflow_save",
	"oauth_connect", "oauth_disconnect", "oauth_flow_status", "oauth_list",
	"email_account_list", "email_message_list", "email_message_read", "email_message_send",
	"share_create_article", "share_create_artifact", "share_list", "share_revoke",
	"vault_secret_delete", "vault_secret_list", "vault_secret_set",
	"recally_article_get", "recally_article_list", "recally_article_save",
	"recally_digest_get", "recally_digest_save",
	"recally_entry_add", "recally_entry_list", "recally_entry_update",
	"recally_feed_add", "recally_feed_list", "recally_feed_poll", "recally_feed_remove",
	"session_create", "session_get", "session_list", "session_send",
	"skill_installed_search", "skill_load",
	"memory_read", "memory_search",
}

func TestListAgentToolsServesEverySplitActionWithAnExactSchema(t *testing.T) {
	env := setupAdmin(t)
	_, sessionID := newNonAdmin(t, env, "split-catalog-user")
	agentID := createAgentAsUser(t, env, sessionID, "split-catalog-agent")
	meta := splitCatalogToolMeta()
	env.rebuild(t, func(deps *server.Deps) {
		deps.BuiltinTools = splitCatalogBuiltins()
		deps.ToolMeta = meta
	})

	rr := doRequestWithSession(t, env.srv, sessionID, http.MethodGet, "/api/agents/"+agentID+"/tools", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list tools status = %d, body: %s", rr.Code, rr.Body.String())
	}
	response := parseResponse(t, rr)
	var list types.AgentToolList
	if err := json.Unmarshal(response.Data, &list); err != nil {
		t.Fatalf("unmarshal tool list: %v", err)
	}

	byName := map[string]types.AgentTool{}
	var builtins []string
	for _, tool := range list.Tools {
		if tool.Source != "builtin" {
			continue
		}
		builtins = append(builtins, tool.Name)
		byName[tool.Name] = tool
	}
	want := slices.Clone(splitCatalog)
	slices.Sort(want)
	slices.Sort(builtins)
	if !slices.Equal(builtins, want) {
		t.Fatalf("builtin catalog = %v,\nwant %v", builtins, want)
	}

	for _, name := range splitCatalog {
		tool := byName[name]
		spec, ok := meta.Lookup(name)
		if !ok {
			t.Fatalf("toolmeta is missing generated builtin %q", name)
		}
		if tool.Family == nil || *tool.Family != spec.Family {
			t.Errorf("%s family = %#v, want toolmeta family %q", name, tool.Family, spec.Family)
		}
		if tool.InputSchema == nil {
			t.Errorf("%s has no input schema", name)
			continue
		}
		schema := *tool.InputSchema
		// The split's contract: a sealed schema per action, and no `action`
		// discriminator left over from the union.
		if sealed, _ := schema["additionalProperties"].(bool); sealed {
			t.Errorf("%s accepts extra properties", name)
		}
		if properties, _ := schema["properties"].(map[string]any); properties["action"] != nil {
			t.Errorf("%s still declares an action property", name)
		}
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("%s has no description", name)
		}
	}
}

// splitCatalogBuiltins registers every family the way cmd/stellad does. The
// services are nil: only the definitions are under test here, and a definition
// is static by construction so the catalog can list a tool without building it.
func splitCatalogToolMeta() *toolmeta.Registry {
	var specs []toolmeta.ActionTool
	specs = append(specs, goal.ActionTools()...)
	specs = append(specs, scheduler.ActionTools()...)
	specs = append(specs, workflowpkg.ActionTools()...)
	specs = append(specs, connections.ActionTools()...)
	specs = append(specs, email.ActionTools()...)
	specs = append(specs, sharepkg.ActionTools()...)
	specs = append(specs, vault.ActionTools()...)
	specs = append(specs, recally.ActionTools()...)
	specs = append(specs, sessionaccess.ActionTools()...)
	specs = append(specs, skillstool.RuntimeActionTools()...)
	specs = append(specs, memory.ActionTools()...)
	return toolmeta.NewRegistry(specs...)
}

func splitCatalogBuiltins() []agent.BuiltinTool {
	var out []agent.BuiltinTool
	add := func(specs []toolmeta.ActionTool, newTool func(toolmeta.ActionTool) pkgtools.Tool) {
		for _, spec := range specs {
			out = append(out, agent.BuiltinTool{Tool: newTool(spec)})
		}
	}
	add(goal.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool { return goal.NewTool(nil, spec) })
	add(scheduler.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool { return scheduler.NewTool(nil, spec) })
	add(workflowpkg.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool { return workflowpkg.NewTool(nil, spec) })
	add(connections.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool { return connections.NewTool(nil, spec) })
	add(email.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool { return email.NewTool(nil, spec) })
	add(sharepkg.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool { return sharepkg.NewTool(nil, spec) })
	add(vault.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool { return vault.NewTool(nil, nil, spec) })
	add(recally.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool { return recally.NewTool(nil, spec) })
	add(sessionaccess.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return sessionaccess.NewTool(nil, spec)
	})
	add(skillstool.RuntimeActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return skillstool.NewAction(nil, spec)
	})
	add(memory.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return memory.NewTool(nil, spec)
	})
	return out
}
