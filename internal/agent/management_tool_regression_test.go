package agent

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/core/toolmeta"
)

type recordingAgentToolHandler struct{ update SettingsAgentUpdateInput }

func (h *recordingAgentToolHandler) Create(context.Context, SettingsAgentCreateInput) (any, error) {
	return nil, nil
}

func (h *recordingAgentToolHandler) Delete(context.Context, SettingsAgentDeleteInput) (any, error) {
	return nil, nil
}

func (h *recordingAgentToolHandler) Get(context.Context, SettingsAgentGetInput) (any, error) {
	return nil, nil
}

func (h *recordingAgentToolHandler) List(context.Context, SettingsAgentListInput) (any, error) {
	return nil, nil
}

func (h *recordingAgentToolHandler) Update(_ context.Context, in SettingsAgentUpdateInput) (any, error) {
	h.update = in
	return nil, nil
}

func TestSettingsAgentUpdateInputPreservesExplicitEmptyStrings(t *testing.T) {
	h := &recordingAgentToolHandler{}
	_, err := SettingsAgentDispatch(context.Background(), h, "update", map[string]any{
		"id": "agent-1", "expected_version": "v1",
		"model": "", "system_prompt": "", "soul": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]*string{
		"model": h.update.Model, "system_prompt": h.update.SystemPrompt, "soul": h.update.Soul,
	} {
		if value == nil || *value != "" {
			t.Errorf("%s = %#v, want explicit empty string", name, value)
		}
	}
}

func TestAgentToolOverridesExcludeSettingsPolicyActions(t *testing.T) {
	registry := toolmeta.NewRegistry(
		toolmeta.ActionTool{Name: "settings_agent_list", Family: "agent", Action: "list"},
		toolmeta.ActionTool{Name: "vault_secret_list", Family: "vault", Action: "list"},
	)
	h := agentOverrideHandler{registry: registry, mcpCatalog: func(context.Context, string, string) map[string]string {
		return map[string]string{"mcp__gh__create_issue": "mcp:gh"}
	}}
	ctx := context.Background()
	if h.managedTool(ctx, "", "settings_agent_list") {
		t.Fatal("Settings action must not be listed, updated, or deleted as an override")
	}
	if !h.managedTool(ctx, "", "vault_secret_list") {
		t.Fatal("ordinary generated action must remain override-managed")
	}
	// An mcp__ name is only managed while the resolved catalog contains it;
	// the prefix alone never admits a stale row.
	if !h.managedTool(ctx, "", "mcp__gh__create_issue") {
		t.Fatal("MCP tool in the resolved catalog must be override-managed")
	}
	if h.managedTool(ctx, "", "mcp__gone__tool") {
		t.Fatal("MCP tool absent from the catalog must not be managed")
	}
}
