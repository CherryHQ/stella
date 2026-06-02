package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/config"
)

const taskJSON = `{"id":"task-1","user_id":"user-1","agent_id":"agent-1","title":"Test task","status":"draft","priority":"routine","review_policy":"none","required":true,"retry_count":0,"max_retries":0,"created_at":"2026-06-02T00:00:00Z","updated_at":"2026-06-02T00:00:00Z"}`

const goalJSON = `{"id":"goal-1","user_id":"user-1","agent_id":"agent-1","title":"Test goal","status":"draft","priority":"routine","review_policy":"none","created_at":"2026-06-02T00:00:00Z","updated_at":"2026-06-02T00:00:00Z"}`

// jsonAPIServer serves a fixed body for the first matching request and points
// the CLI's config at itself.
func jsonAPIServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	t.Setenv("STELLA_TOKEN", "test-token")
	t.Setenv("STELLA_AGENT_ID", "agent-1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	t.Setenv("STELLA_SERVER_URL", server.URL)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	return server
}

func runTaskApp(t *testing.T, args ...string) string {
	t.Helper()
	out := &bytes.Buffer{}
	app := ucli.NewApp()
	app.Writer = out
	app.Commands = []*ucli.Command{taskCommand()}
	if err := app.Run(append([]string{"stella"}, args...)); err != nil {
		t.Fatalf("Run %v: %v", args, err)
	}
	return out.String()
}

func TestTaskGetJSON(t *testing.T) {
	jsonAPIServer(t, taskJSON)
	out := runTaskApp(t, "task", "get", "--json", "task-1")
	var got struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%q)", err, out)
	}
	if got.ID != "task-1" || got.Title != "Test task" {
		t.Fatalf("got %+v", got)
	}
}

func TestTaskListJSONReturnsEnvelope(t *testing.T) {
	jsonAPIServer(t, `{"tasks":[`+taskJSON+`]}`)
	out := runTaskApp(t, "task", "list", "--json")
	var got struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output not valid JSON envelope: %v (%q)", err, out)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].ID != "task-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestTaskBlockerResolveJSON(t *testing.T) {
	jsonAPIServer(t, taskJSON)
	out := runTaskApp(t, "task", "blocker", "resolve", "--json", "task-1", "blocker-1")
	var got struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%q)", err, out)
	}
	if got.ID != "task-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestGoalGetJSON(t *testing.T) {
	jsonAPIServer(t, goalJSON)
	out := runTaskApp(t, "task", "goal", "get", "--json", "goal-1")
	var got struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%q)", err, out)
	}
	if got.ID != "goal-1" || got.Title != "Test goal" {
		t.Fatalf("got %+v", got)
	}
}
