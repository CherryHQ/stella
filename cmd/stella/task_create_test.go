package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/config"
)

func TestTaskCreateUsesAgentIDFromEnv(t *testing.T) {
	setTestScopedToken(t, "agent-1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/tasks" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Title   string `json:"title"`
			AgentID string `json:"agent_id"`
			GoalID  string `json:"goal_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Title != "Test task" || body.AgentID != "agent-1" || body.GoalID != "goal-1" {
			t.Fatalf("unexpected body: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task-1","user_id":"user-1","agent_id":"agent-1","goal_id":"goal-1","title":"Test task","status":"draft","priority":"routine","review_policy":"none","required":true,"retry_count":0,"max_retries":0,"created_at":"2026-06-02T00:00:00Z","updated_at":"2026-06-02T00:00:00Z"}`))
	}))
	defer server.Close()
	t.Setenv("STELLA_SERVER_URL", server.URL)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	app := ucli.NewApp()
	app.Commands = []*ucli.Command{taskCommand()}
	if err := app.Run([]string{"stella", "task", "create", "--title", "Test task", "--goal-id", "goal-1"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestTaskCreateRequiresAgentID(t *testing.T) {
	app := ucli.NewApp()
	app.Commands = []*ucli.Command{taskCommand()}
	err := app.Run([]string{"stella", "task", "create", "--title", "Test task"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTaskListUsesAgentIDFromEnv(t *testing.T) {
	setTestScopedToken(t, "agent-1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tasks" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("agent_id"); got != "agent-1" {
			t.Fatalf("agent_id query = %q", got)
		}
		if got := r.URL.Query().Get("project_id"); got != "project-1" {
			t.Fatalf("project_id query = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tasks":[]}`))
	}))
	defer server.Close()
	t.Setenv("STELLA_SERVER_URL", server.URL)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	app := ucli.NewApp()
	app.Commands = []*ucli.Command{taskCommand()}
	if err := app.Run([]string{"stella", "task", "list", "--project-id", "project-1"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestTaskGetRequiresAgentID(t *testing.T) {
	app := ucli.NewApp()
	app.Commands = []*ucli.Command{taskCommand()}
	err := app.Run([]string{"stella", "task", "get", "task-1"})
	if err == nil {
		t.Fatal("expected error")
	}
}
