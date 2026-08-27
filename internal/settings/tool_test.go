package settings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
)

type fakeAgentReader struct {
	agents []config.Agent
}

func (f fakeAgentReader) ListReadable(context.Context, authz.Authority, bool) ([]config.Agent, error) {
	return f.agents, nil
}

func (f fakeAgentReader) Read(_ context.Context, _ authz.Authority, id string) (config.Agent, error) {
	for _, ag := range f.agents {
		if ag.ID == id {
			return ag, nil
		}
	}
	return config.Agent{}, errors.New("not found")
}

func userAuthority(t *testing.T, id string) authz.Authority {
	t.Helper()
	authority, err := authz.NewUserAuthority(authz.UserID(id), false)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func settingsContext(authority authz.Authority) context.Context {
	ctx := context.Background()
	ctx = authz.WithUserID(ctx, string(authority.UserID()))
	ctx = authz.WithAgentID(ctx, AgentID)
	ctx = memory.WithSessionID(ctx, "session-1")
	return authz.WithAuthority(ctx, authority)
}

func TestAvailableOnlyForStellaDirectSessions(t *testing.T) {
	cases := []struct {
		name   string
		params agent.RunnerParams
		want   bool
	}{
		{"stella dm", agent.RunnerParams{UserID: "u1", AgentID: AgentID, ForegroundHuman: true}, true},
		{"ordinary agent", agent.RunnerParams{UserID: "u1", AgentID: "writer", ForegroundHuman: true}, false},
		{"missing user", agent.RunnerParams{AgentID: AgentID, ForegroundHuman: true}, false},
		{"group", agent.RunnerParams{UserID: "u1", AgentID: AgentID, GroupID: "g1", ForegroundHuman: true}, false},
		{"guest", agent.RunnerParams{UserID: "u1", AgentID: AgentID, GuestID: "g1", ForegroundHuman: true}, false},
		{"scheduler", agent.RunnerParams{UserID: "u1", AgentID: AgentID}, false},
		{"task", agent.RunnerParams{UserID: "u1", AgentID: AgentID}, false},
		{"delegate", agent.RunnerParams{UserID: "u1", AgentID: AgentID}, false},
		{"webhook", agent.RunnerParams{UserID: "u1", AgentID: AgentID}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Available(context.Background(), tc.params); got != tc.want {
				t.Fatalf("Available() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExecuteRequiresCurrentTurnAuthority(t *testing.T) {
	agents := fakeAgentReader{agents: []config.Agent{{ID: "stella", Name: "Stella", Model: "openai/gpt", Enabled: true}}}
	tool := &Tool{agents: agents}
	authority := userAuthority(t, "u1")

	if _, err := tool.Execute(context.Background(), map[string]any{"action": "catalog"}); err == nil {
		t.Fatal("catalog without a turn authority must fail")
	}
	if _, err := tool.Execute(settingsContext(authority), map[string]any{"action": "catalog"}); err != nil {
		t.Fatalf("catalog with a valid turn authority: %v", err)
	}

	wrong := settingsContext(userAuthority(t, "u2"))
	wrong = authz.WithUserID(wrong, "u1")
	if _, err := tool.Execute(wrong, map[string]any{"action": "catalog"}); err == nil {
		t.Fatal("authority for another user must fail")
	}
}

func TestReadAgentsUsesPEPAndRedactsOpaqueFields(t *testing.T) {
	agents := fakeAgentReader{agents: []config.Agent{{
		ID: "writer", Name: "Writer", Model: "openai/gpt", SystemPrompt: "prompt", Soul: "soul",
		Workspace: "/private", Sandbox: config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}}, CreatorID: "owner", Enabled: true,
	}}}
	tool := &Tool{agents: agents}
	result, err := tool.Execute(settingsContext(userAuthority(t, "u1")), map[string]any{
		"action": "get", "resource": "agents", "id": "writer",
	})
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if _, ok := got["workspace"]; ok {
		t.Fatal("agent workspace must not be exposed")
	}
	if _, ok := got["sandbox"]; ok {
		t.Fatal("agent sandbox must not be exposed")
	}
	if got["id"] != "writer" {
		t.Fatalf("id = %v, want writer", got["id"])
	}
}

func TestReadAgentsPaginatesAndBoundsSummaries(t *testing.T) {
	long := strings.Repeat("x", maxAgentDetailTextBytes+100)
	agents := make([]config.Agent, maxAgentListPageSize+1)
	for i := range agents {
		agents[i] = config.Agent{ID: "agent-" + string(rune('a'+i%26)), SystemPrompt: long, Soul: long, Enabled: true}
	}
	tool := &Tool{agents: fakeAgentReader{agents: agents}}
	result, err := tool.Execute(settingsContext(userAuthority(t, "u1")), map[string]any{
		"action": "list", "resource": "agents", "page_size": maxAgentListPageSize,
	})
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	var got struct {
		Agents        []map[string]any `json:"agents"`
		Total         int              `json:"total"`
		NextPageToken string           `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(got.Agents) != maxAgentListPageSize || got.Total != len(agents) || got.NextPageToken == "" {
		t.Fatalf("list metadata = len %d total %d next_page_token %q", len(got.Agents), got.Total, got.NextPageToken)
	}
	if len(got.Agents[0]["system_prompt"].(string)) > maxAgentListSummaryBytes ||
		got.Agents[0]["system_prompt_truncated"] != true || got.Agents[0]["soul_truncated"] != true {
		t.Fatalf("list did not return bounded prompt summaries: %#v", got.Agents[0])
	}

	result, err = tool.Execute(settingsContext(userAuthority(t, "u1")), map[string]any{
		"action": "list", "resource": "agents", "page_size": 1, "page_token": got.NextPageToken,
	})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	var secondPage struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal([]byte(result), &secondPage); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(secondPage.Agents) != 1 {
		t.Fatalf("second page len=%d, want 1", len(secondPage.Agents))
	}
}

func TestGetAgentBoundsLongTextAndMarksTruncation(t *testing.T) {
	long := strings.Repeat("x", maxAgentDetailTextBytes+100)
	tool := &Tool{agents: fakeAgentReader{agents: []config.Agent{{ID: "writer", SystemPrompt: long, Soul: long}}}}
	result, err := tool.Execute(settingsContext(userAuthority(t, "u1")), map[string]any{
		"action": "get", "resource": "agents", "id": "writer",
	})
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(got["system_prompt"].(string)) > maxAgentDetailTextBytes || got["system_prompt_truncated"] != true || got["soul_truncated"] != true {
		t.Fatalf("get did not return bounded prompt text: %#v", got)
	}
}

func TestReadAgentsBoundsEveryProjectedTextField(t *testing.T) {
	long := strings.Repeat("x", maxAgentProjectedTextBytes+100)
	id := "agent-" + long
	authority, err := authz.NewUserAuthority("admin", true)
	if err != nil {
		t.Fatal(err)
	}
	tool := &Tool{agents: fakeAgentReader{agents: []config.Agent{{
		ID: id, Name: long, Model: long, Scope: long, CreatorID: long,
		SystemPrompt: strings.Repeat("p", maxAgentDetailTextBytes+100),
		Soul:         strings.Repeat("s", maxAgentDetailTextBytes+100),
	}}}}
	result, err := tool.Execute(settingsContext(authority), map[string]any{
		"action": "get", "resource": "agents", "id": id,
	})
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if len(result) > maxAgentSerializedResult {
		t.Fatalf("serialized get bytes=%d, limit=%d", len(result), maxAgentSerializedResult)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "name", "model", "scope", "creator_id"} {
		if got[field].(string) == "" || len(got[field].(string)) > maxAgentProjectedTextBytes {
			t.Fatalf("%s bytes=%d, want 1..%d", field, len(got[field].(string)), maxAgentProjectedTextBytes)
		}
	}
}

func TestReadAgentsEnforcesSerializedListCeiling(t *testing.T) {
	long := strings.Repeat("x", maxAgentProjectedTextBytes+100)
	agents := make([]config.Agent, maxAgentListPageSize)
	for i := range agents {
		agents[i] = config.Agent{
			ID: "id-" + long, Name: long, Model: long, Scope: long,
			SystemPrompt: long, Soul: long, Enabled: true,
		}
	}
	tool := &Tool{agents: fakeAgentReader{agents: agents}}
	result, err := tool.Execute(settingsContext(userAuthority(t, "u1")), map[string]any{
		"action": "list", "resource": "agents", "page_size": maxAgentListPageSize,
	})
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if len(result) > maxAgentSerializedResult {
		t.Fatalf("serialized list bytes=%d, limit=%d", len(result), maxAgentSerializedResult)
	}
	var got struct {
		Agents        []map[string]any `json:"agents"`
		NextPageToken string           `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) >= maxAgentListPageSize || got.NextPageToken == "" {
		t.Fatalf("ceiling did not preserve a continuation: len=%d next=%q", len(got.Agents), got.NextPageToken)
	}
}

func TestExecuteRejectsMutationAndUnknownFields(t *testing.T) {
	tool := &Tool{agents: fakeAgentReader{}}
	ctx := settingsContext(userAuthority(t, "u1"))
	for _, args := range []map[string]any{
		{"action": "preview", "resource": "agents"},
		{"action": "catalog", "token": "secret"},
	} {
		if _, err := tool.Execute(ctx, args); err == nil {
			t.Fatalf("Execute(%v) must fail", args)
		}
	}
}
