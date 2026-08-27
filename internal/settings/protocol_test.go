package settings

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
)

type recordingAgentMutation struct {
	created []config.Agent
	updated []config.Agent
	deleted []string
}

func (m *recordingAgentMutation) Create(_ context.Context, _ authz.Authority, a config.Agent) (config.Agent, error) {
	m.created = append(m.created, a)
	return a, nil
}

func (m *recordingAgentMutation) Update(_ context.Context, _ authz.Authority, a config.Agent) (config.Agent, error) {
	m.updated = append(m.updated, a)
	return a, nil
}

func (m *recordingAgentMutation) Delete(_ context.Context, _ authz.Authority, id string) error {
	m.deleted = append(m.deleted, id)
	return nil
}

func (m *recordingAgentMutation) UpdateIfUnchanged(ctx context.Context, authority authz.Authority, _ config.Agent, candidate config.Agent) (config.Agent, error) {
	return m.Update(ctx, authority, candidate)
}

func (m *recordingAgentMutation) DeleteIfUnchanged(ctx context.Context, authority authz.Authority, expected config.Agent) error {
	return m.Delete(ctx, authority, expected.ID)
}

func tokenFromResult(t *testing.T, raw string) string {
	t.Helper()
	var result struct {
		Token string `json:"confirmation_token"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Token == "" {
		t.Fatal("preview returned no confirmation token")
	}
	return result.Token
}

func TestDescribeExposesEveryMutationContract(t *testing.T) {
	tool := NewTool(&mutableAgentReader{agent: config.Agent{ID: "writer", Scope: config.AgentScopeRestricted}})
	ctx := settingsContext(userAuthority(t, "u1"))
	for _, resource := range []string{"agents", "library", "skills", "tool_overrides"} {
		t.Run(resource, func(t *testing.T) {
			result, err := tool.Execute(ctx, map[string]any{"action": "describe", "resource": resource})
			if err != nil {
				t.Fatalf("describe: %v", err)
			}
			var decoded struct {
				Contracts map[string]struct {
					Required      []string   `json:"required"`
					Optional      []string   `json:"optional"`
					RequiredAnyOf [][]string `json:"required_any_of"`
					Constraints   []string   `json:"constraints"`
				} `json:"operation_contracts"`
			}
			if err := json.Unmarshal([]byte(result), &decoded); err != nil {
				t.Fatalf("decode describe: %v", err)
			}
			if len(decoded.Contracts) == 0 {
				t.Fatal("describe returned no operation contracts")
			}
			for operation, contract := range decoded.Contracts {
				if contract.Required == nil || contract.Optional == nil || contract.Constraints == nil || len(contract.Constraints) == 0 {
					t.Fatalf("%s contract is incomplete: %#v", operation, contract)
				}
			}
			if resource == "skills" {
				create := decoded.Contracts["create"]
				if len(create.RequiredAnyOf) != 1 || len(create.RequiredAnyOf[0]) != 2 {
					t.Fatalf("skills.create any-of contract = %#v", create.RequiredAnyOf)
				}
			}
		})
	}
}

func TestMutationContractsRejectMissingAndUnsupportedFields(t *testing.T) {
	cases := []struct {
		name      string
		resource  string
		operation string
		input     map[string]any
		wantErr   bool
	}{
		{name: "agent create", resource: "agents", operation: "create", input: map[string]any{"name": "Writer"}},
		{name: "agent create rejects id", resource: "agents", operation: "create", input: map[string]any{"name": "Writer", "id": "caller-owned"}, wantErr: true},
		{name: "override set requires enabled", resource: "tool_overrides", operation: "set", input: map[string]any{"tool_name": "memory", "scope": "user"}, wantErr: true},
		{name: "override false is present", resource: "tool_overrides", operation: "set", input: map[string]any{"tool_name": "memory", "scope": "user", "enabled": false}},
		{name: "skill requires body or files", resource: "skills", operation: "create", input: map[string]any{"scope": "user", "name": "x"}, wantErr: true},
		{name: "skill body satisfies any-of", resource: "skills", operation: "create", input: map[string]any{"scope": "user", "name": "x", "body": "body"}},
		{name: "operation-specific unknown field", resource: "library", operation: "delete", input: map[string]any{"id": "f", "content": "ignored"}, wantErr: true},
		{name: "unknown operation", resource: "library", operation: "update", input: map[string]any{"id": "f"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMutationContract(tc.resource, tc.operation, tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateMutationContract() = %v, wantErr=%t", err, tc.wantErr)
			}
		})
	}
}

func TestMutationScopeMatrix(t *testing.T) {
	ordinary := userAuthority(t, "u1")
	admin, err := authz.NewUserAuthority("admin", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"system", "system_agent"} {
		if err := authorizeMutationScope(ordinary, map[string]any{"scope": scope}); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("ordinary %s scope = %v, want forbidden", scope, err)
		}
		if err := authorizeMutationScope(admin, map[string]any{"scope": scope}); err != nil {
			t.Fatalf("admin %s scope = %v", scope, err)
		}
	}
	for _, scope := range []string{"", "restricted", "user", "user_agent"} {
		if err := authorizeMutationScope(ordinary, map[string]any{"scope": scope}); err != nil {
			t.Fatalf("ordinary %s scope = %v", scope, err)
		}
	}
}

func TestConfirmRechecksDeploymentScopeMatrix(t *testing.T) {
	tool := NewTool(&mutableAgentReader{agent: config.Agent{ID: "writer", Scope: config.AgentScopeRestricted}})
	ctx := settingsContext(userAuthority(t, "u1"))
	tool.tokens["scope-token"] = pendingMutation{
		userID: "u1", sessionID: currentSessionID(ctx), agentID: "stella",
		resource: "skills", operation: "create", input: json.RawMessage(`{"scope":"system","name":"x","body":"x"}`),
		expiresAt: time.Now().Add(time.Minute),
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "confirm", "token": "scope-token"}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("confirm error = %v, want forbidden", err)
	}
}

func TestMutationPreviewConfirmReauthorizesAndConsumesToken(t *testing.T) {
	current := config.Agent{ID: "writer", Name: "Writer", Model: "openai/gpt", Enabled: true}
	reader := &mutableAgentReader{agent: current}
	mutation := &recordingAgentMutation{}
	tool := NewTool(reader, WithAgentMutations(mutation))
	ctx := settingsContext(userAuthority(t, "u1"))

	preview, err := tool.Execute(ctx, map[string]any{"action": "preview", "resource": "agents", "operation": "update", "input": map[string]any{
		"id": "writer", "name": "Updated",
	}})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	token := tokenFromResult(t, preview)
	if _, err := tool.Execute(ctx, map[string]any{"action": "confirm", "token": token}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if len(mutation.updated) != 1 || mutation.updated[0].Name != "Updated" {
		t.Fatalf("updates = %#v", mutation.updated)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "confirm", "token": token}); err == nil {
		t.Fatal("single-use token was accepted twice")
	}
}

type mutableAgentReader struct{ agent config.Agent }

func (r *mutableAgentReader) ListReadable(context.Context, authz.Authority, bool) ([]config.Agent, error) {
	return []config.Agent{r.agent}, nil
}

func (r *mutableAgentReader) Read(_ context.Context, _ authz.Authority, id string) (config.Agent, error) {
	if id != r.agent.ID {
		return config.Agent{}, errors.New("not found")
	}
	return r.agent, nil
}

func TestOrdinaryUserCannotWriteDeploymentScopesAcrossMutationResources(t *testing.T) {
	tool := NewTool(&mutableAgentReader{agent: config.Agent{ID: "writer", Scope: config.AgentScopeRestricted}})
	ctx := settingsContext(userAuthority(t, "u1"))
	cases := []struct {
		resource  string
		operation string
		input     map[string]any
	}{
		{"agents", "create", map[string]any{"name": "system", "scope": config.AgentScopeSystem}},
		{"library", "create", map[string]any{"scope": "system", "file_name": "x.md", "content": "x"}},
		{"skills", "create", map[string]any{"scope": "system", "name": "system", "body": "x"}},
		{"tool_overrides", "set", map[string]any{"scope": "system", "tool_name": "memory", "enabled": false}},
	}
	for _, tc := range cases {
		t.Run(tc.resource, func(t *testing.T) {
			_, err := tool.Execute(ctx, map[string]any{"action": "preview", "resource": tc.resource, "operation": tc.operation, "input": tc.input})
			if !errors.Is(err, authz.ErrForbidden) {
				t.Fatalf("preview error = %v, want forbidden", err)
			}
		})
	}
}

func TestOrdinaryUserCannotWriteSystemAgentScope(t *testing.T) {
	reader := &mutableAgentReader{agent: config.Agent{ID: "system-agent", Name: "System", Scope: config.AgentScopeSystem}}
	mutation := &recordingAgentMutation{}
	tool := NewTool(reader, WithAgentMutations(mutation))
	ctx := settingsContext(userAuthority(t, "u1"))

	if _, err := tool.Execute(ctx, map[string]any{"action": "preview", "resource": "agents", "operation": "create", "input": map[string]any{
		"name": "Should fail", "scope": config.AgentScopeSystem,
	}}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("system create error = %v, want forbidden", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "preview", "resource": "agents", "operation": "update", "input": map[string]any{
		"id": "system-agent", "name": "Should fail",
	}}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("system update error = %v, want forbidden", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "preview", "resource": "agents", "operation": "delete", "input": map[string]any{
		"id": "system-agent",
	}}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("system delete error = %v, want forbidden", err)
	}
}

func TestMutationConfirmRejectsStaleAgentWithoutWriting(t *testing.T) {
	reader := &mutableAgentReader{agent: config.Agent{ID: "writer", Name: "Writer"}}
	mutation := &recordingAgentMutation{}
	tool := NewTool(reader, WithAgentMutations(mutation))
	ctx := settingsContext(userAuthority(t, "u1"))
	preview, err := tool.Execute(ctx, map[string]any{"action": "preview", "resource": "agents", "operation": "delete", "input": map[string]any{"id": "writer"}})
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromResult(t, preview)
	reader.agent.Name = "Changed"
	if _, err := tool.Execute(ctx, map[string]any{"action": "confirm", "token": token}); err == nil {
		t.Fatal("stale confirmation succeeded")
	}
	if len(mutation.deleted) != 0 {
		t.Fatalf("deleted = %#v", mutation.deleted)
	}
}

func TestMutationTokenIsBoundToSessionAndUser(t *testing.T) {
	reader := &mutableAgentReader{agent: config.Agent{ID: "writer", Name: "Writer"}}
	tool := NewTool(reader, WithAgentMutations(&recordingAgentMutation{}))
	ctx := settingsContext(userAuthority(t, "u1"))
	preview, err := tool.Execute(ctx, map[string]any{"action": "preview", "resource": "agents", "operation": "delete", "input": map[string]any{"id": "writer"}})
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromResult(t, preview)
	other := settingsContext(userAuthority(t, "u2"))
	if _, err := tool.Execute(other, map[string]any{"action": "confirm", "token": token}); err == nil {
		t.Fatal("foreign user consumed token")
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "confirm", "token": token}); err == nil {
		t.Fatal("token should be consumed on a mismatched attempt")
	}
}
