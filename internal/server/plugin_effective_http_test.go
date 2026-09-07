package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/server"
)

func TestPluginEffectiveIncludesAdministrativeCapsForOrdinaryUser(t *testing.T) {
	env := setupAdmin(t)
	catalog := plugin.NewCatalog()
	spec, err := plugin.PublishDefinitionSpec([]byte(`{"prompt":"private system guidance"}`))
	if err != nil {
		t.Fatal(err)
	}
	def := plugin.Definition{ID: "capped", DisplayName: "Capped", Source: plugin.SourceBuiltin, DefaultEnabled: true, Revision: 1, Spec: spec}
	if err := catalog.Register(def); err != nil {
		t.Fatal(err)
	}
	svc := plugin.NewService(env.db, env.deps.AgentAccess, catalog, plugin.BackendPolicy{}, func(_ context.Context, mutate func() error) error { return mutate() })
	if err := svc.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	env.rebuild(t, func(d *server.Deps) { d.PluginService = svc })
	_, token := newNonAdmin(t, env, "capped-user")
	agentID := createAgentAsUser(t, env, token, "Capped agent")
	for _, scope := range []string{"system", "system_agent"} {
		t.Run(scope, func(t *testing.T) {
			if _, err := env.db.Exec(t.Context(), `UPDATE plugin_config SET enabled=true WHERE plugin_id='capped'`); err != nil {
				t.Fatal(err)
			}
			if scope == "system" {
				if _, err := env.db.Exec(t.Context(), `UPDATE plugin_config SET enabled=false WHERE plugin_id='capped' AND scope='system'`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := env.db.Exec(t.Context(), `INSERT INTO plugin_config(plugin_id,scope,agent_id,enabled,config,credential_refs,revision) VALUES ('capped','system_agent',$1,false,'{}','{}',1)`, agentID); err != nil {
					t.Fatal(err)
				}
			}
			rr := doRequestWithSession(t, env.srv, token, http.MethodGet, "/api/plugins/capped/effective?agent_id="+agentID, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("effective = %d: %s", rr.Code, rr.Body.String())
			}
			var got apitypes.PluginEffective
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.IsEffectivelyEnabled {
				t.Fatalf("administrative cap omitted: %+v", got)
			}
			if strings.Contains(rr.Body.String(), "private system guidance") || strings.Contains(rr.Body.String(), "payload") {
				t.Fatalf("private payload exposed: %s", rr.Body.String())
			}
		})
	}
	_, otherToken := newNonAdmin(t, env, "other-capped-user")
	rr := doRequestWithSession(t, env.srv, otherToken, http.MethodGet, "/api/plugins/capped/effective?agent_id="+agentID, nil)
	if rr.Code != http.StatusForbidden && rr.Code != http.StatusNotFound {
		t.Fatalf("foreign agent effective = %d", rr.Code)
	}
}
