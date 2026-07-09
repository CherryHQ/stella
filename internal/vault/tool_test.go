package vault_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type fakeToolInvalidator struct {
	err   error
	calls []string
}

func (f *fakeToolInvalidator) InvalidateAll() error {
	f.calls = append(f.calls, "all")
	return f.err
}

func (f *fakeToolInvalidator) InvalidateAgent(agentID string) error {
	f.calls = append(f.calls, "agent:"+agentID)
	return f.err
}

func (f *fakeToolInvalidator) InvalidateUser(userID string) error {
	f.calls = append(f.calls, "user:"+userID)
	return f.err
}

func TestVaultToolMutationsInvalidateRunners(t *testing.T) {
	ctx := context.Background()

	for _, tt := range []struct {
		name       string
		action     string
		scope      string
		secret     string
		value      string
		precreate  bool
		wantStatus string
	}{
		{name: "set default user scope", action: "set", secret: "SET_USER_SECRET", value: "user-value", wantStatus: "set"},
		{name: "set user agent scope", action: "set", scope: vault.ScopeUserAgent, secret: "SET_AGENT_SECRET", value: "agent-value", wantStatus: "set"},
		{name: "delete default user scope", action: "delete", secret: "DELETE_USER_SECRET", precreate: true, wantStatus: "deleted"},
		{name: "delete user agent scope", action: "delete", scope: vault.ScopeUserAgent, secret: "DELETE_AGENT_SECRET", precreate: true, wantStatus: "deleted"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, userID, q := testServiceWithQueries(t)
			agentID := "agent-1"
			createToolTestAgent(t, q, agentID)
			if tt.precreate {
				if err := svc.SetScoped(ctx, vaultScopeOrDefault(tt.scope), userID, agentIDForScope(tt.scope, agentID), tt.secret, "old-value"); err != nil {
					t.Fatalf("precreate secret: %v", err)
				}
			}

			inv := &fakeToolInvalidator{}
			tool := vault.NewTool(svc, inv)
			args := map[string]any{"action": tt.action, "name": tt.secret}
			if tt.scope != "" {
				args["scope"] = tt.scope
			}
			if tt.action == "set" {
				args["value"] = tt.value
			}
			out, err := tool.Execute(vaultToolContext(userID, agentID), args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			var resp struct {
				Name   string `json:"name"`
				Scope  string `json:"scope"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(out), &resp); err != nil {
				t.Fatalf("unmarshal response %q: %v", out, err)
			}
			if resp.Name != tt.secret || resp.Scope != vaultScopeOrDefault(tt.scope) || resp.Status != tt.wantStatus {
				t.Fatalf("response = %+v, want name=%s scope=%s status=%s", resp, tt.secret, vaultScopeOrDefault(tt.scope), tt.wantStatus)
			}
			wantCalls := []string{"user:" + userID}
			if !reflect.DeepEqual(inv.calls, wantCalls) {
				t.Fatalf("invalidation calls = %v, want %v", inv.calls, wantCalls)
			}
		})
	}
}

func TestVaultToolNilInvalidatorSafe(t *testing.T) {
	svc, _, userID := testService(t)
	tool := vault.NewTool(svc, nil)

	if _, err := tool.Execute(vaultToolContext(userID, "agent-1"), map[string]any{
		"action": "set",
		"name":   "NIL_INVALIDATOR_SECRET",
		"value":  "ok",
	}); err != nil {
		t.Fatalf("Execute with nil invalidator: %v", err)
	}
}

func TestVaultToolInvalidationFailureDoesNotFailMutation(t *testing.T) {
	svc, _, userID := testService(t)
	inv := &fakeToolInvalidator{err: errors.New("runner cache offline")}
	tool := vault.NewTool(svc, inv)

	if _, err := tool.Execute(vaultToolContext(userID, "agent-1"), map[string]any{
		"action": "set",
		"name":   "INVALIDATION_ERROR_SECRET",
		"value":  "still-written",
	}); err != nil {
		t.Fatalf("Execute with invalidation error: %v", err)
	}
	got, err := svc.GetScoped(context.Background(), vault.ScopeUser, userID, "", "INVALIDATION_ERROR_SECRET")
	if err != nil {
		t.Fatalf("GetScoped: %v", err)
	}
	if got != "still-written" {
		t.Fatalf("secret = %q, want still-written", got)
	}
}

func TestInvalidateForScope(t *testing.T) {
	if err := vault.InvalidateForScope(nil, vault.ScopeUser, "user-1", ""); err != nil {
		t.Fatalf("InvalidateForScope with nil invalidator: %v", err)
	}

	for _, tt := range []struct {
		name      string
		scope     string
		userID    string
		agentID   string
		wantCalls []string
	}{
		{name: "user", scope: vault.ScopeUser, userID: "user-1", wantCalls: []string{"user:user-1"}},
		{name: "empty default user", scope: "", userID: "user-1", wantCalls: []string{"user:user-1"}},
		{name: "user agent reaches user runners", scope: vault.ScopeUserAgent, userID: "user-1", agentID: "agent-1", wantCalls: []string{"user:user-1"}},
		{name: "system agent reaches agent runners", scope: vault.ScopeSystemAgent, agentID: "agent-1", wantCalls: []string{"agent:agent-1"}},
		{name: "system reaches all runners", scope: vault.ScopeSystem, wantCalls: []string{"all"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inv := &fakeToolInvalidator{}
			if err := vault.InvalidateForScope(inv, tt.scope, tt.userID, tt.agentID); err != nil {
				t.Fatalf("InvalidateForScope: %v", err)
			}
			if !reflect.DeepEqual(inv.calls, tt.wantCalls) {
				t.Fatalf("calls = %v, want %v", inv.calls, tt.wantCalls)
			}
		})
	}
}

func vaultToolContext(userID, agentID string) context.Context {
	ctx := context.Background()
	ctx = authz.WithUserID(ctx, userID)
	ctx = authz.WithAgentID(ctx, agentID)
	return ctx
}

func vaultScopeOrDefault(scope string) string {
	if scope == "" {
		return vault.ScopeUser
	}
	return scope
}

func agentIDForScope(scope string, agentID string) string {
	if scope == vault.ScopeUserAgent {
		return agentID
	}
	return ""
}

func createToolTestAgent(t *testing.T, q *sqlc.Queries, agentID string) {
	t.Helper()
	if _, err := q.CreateAgent(context.Background(), sqlc.CreateAgentParams{
		ID: agentID, Name: "Tool Test Agent", Model: "test/model", Workspace: "workspace", Sandbox: json.RawMessage("{}"), EnabledBuiltinSkills: json.RawMessage("[]"), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
}
