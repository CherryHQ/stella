package settings

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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
