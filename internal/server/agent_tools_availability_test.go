package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/config"
)

func TestListAgentToolsReflectsVisionAvailability(t *testing.T) {
	env := setupAdmin(t)
	_, sessionID := newNonAdmin(t, env, "vision-tools-user")
	agentID := createAgentAsUser(t, env, sessionID, "vision-tools-agent")

	assertVLLMEnabled(t, env, sessionID, agentID, false)

	ctx := context.Background()
	if err := env.store.CreateProvider(ctx, config.Provider{
		ID:      "vision-tools-provider",
		Type:    "openai-completions",
		Name:    "Vision tools provider",
		Enabled: true,
		APIKey:  "test-key",
	}); err != nil {
		t.Fatalf("create vision provider: %v", err)
	}
	if err := config.SaveVisionSettings(ctx, env.store, config.VisionSettings{Model: "vision-tools-provider/test-model"}); err != nil {
		t.Fatalf("save vision settings: %v", err)
	}

	assertVLLMEnabled(t, env, sessionID, agentID, true)
}

func assertVLLMEnabled(t *testing.T, env *testEnv, sessionID, agentID string, want bool) {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, sessionID, http.MethodGet, "/api/agents/"+agentID+"/tools", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list tools status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	response := parseResponse(t, rr)
	var list types.AgentToolList
	if err := json.Unmarshal(response.Data, &list); err != nil {
		t.Fatalf("unmarshal tool list: %v", err)
	}
	for _, tool := range list.Tools {
		if tool.Name == "vllm" {
			if tool.Source != "core" {
				t.Fatalf("vllm source = %q, want core", tool.Source)
			}
			if tool.Enabled != want {
				t.Fatalf("vllm enabled = %t, want %t", tool.Enabled, want)
			}
			return
		}
	}
	t.Fatal("vllm missing from core tool catalog")
}
