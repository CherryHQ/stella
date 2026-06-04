package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/config"
)

func TestGoalCreateUsesAgentIDFromEnv(t *testing.T) {
	setTestScopedToken(t, "agent-1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/goals" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Title   string `json:"title"`
			AgentID string `json:"agent_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Title != "Test goal" || body.AgentID != "agent-1" {
			t.Fatalf("unexpected body: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"goal-1","user_id":"user-1","agent_id":"agent-1","title":"Test goal","status":"draft","priority":"routine","review_policy":"none","created_at":"2026-06-02T00:00:00Z","updated_at":"2026-06-02T00:00:00Z"}`))
	}))
	defer server.Close()
	t.Setenv("STELLA_SERVER_URL", server.URL)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	app := ucli.NewApp()
	app.Commands = []*ucli.Command{taskCommand()}
	if err := app.Run([]string{"stella", "task", "goal", "create", "--title", "Test goal"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestGoalCreateRequiresAgentID(t *testing.T) {
	app := ucli.NewApp()
	app.Commands = []*ucli.Command{taskCommand()}
	err := app.Run([]string{"stella", "task", "goal", "create", "--title", "Test goal"})
	if err == nil {
		t.Fatal("expected error")
	}
}
