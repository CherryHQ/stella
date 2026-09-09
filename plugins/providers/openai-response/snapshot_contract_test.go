package openairesponse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
)

func TestProviderFinalToolSnapshotsExecuteOnce(t *testing.T) {
	const call = `{"id":"fc_1","type":"function_call","call_id":"call_1","name":"bash","arguments":"{\"command\":\"printf ready\"}","status":"completed"}`
	const added = `{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"bash","arguments":""}}`
	const delta = `{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"command\":"}`
	const argsDone = `{"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"command\":\"printf ready\"}"}`
	itemDone := `{"type":"response.output_item.done","item":` + call + `}`
	completed := `{"type":"response.completed","response":{"status":"completed","output":[` + call + `]}}`
	for _, tc := range []struct {
		name   string
		events []string
	}{
		{"item snapshot", []string{itemDone, completed}},
		{"response snapshot", []string{completed}},
		{"arguments snapshot", []string{added, argsDone, completedResponseEvent("done", 1, 1, 2)}},
		{"partial deltas and snapshots", []string{added, delta, argsDone, itemDone, completed}},
		{"repeated snapshots", []string{itemDone, itemDone, completed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, executions atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Input []struct {
						Type      string `json:"type"`
						CallID    string `json:"call_id"`
						Arguments string `json:"arguments"`
						Output    string `json:"output"`
					} `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if requests.Add(1) == 1 {
					_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"I will check now.\"}\n\n")
					for _, event := range tc.events {
						_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
					}
					return
				}
				var calls, outputs int
				for _, item := range body.Input {
					switch item.Type {
					case "function_call":
						calls++
						if item.CallID != "call_1" || item.Arguments != `{"command":"printf ready"}` {
							t.Errorf("wrong call round trip: %+v", item)
						}
					case "function_call_output":
						outputs++
						if item.CallID != "call_1" || item.Output != "ready" {
							t.Errorf("wrong output round trip: %+v", item)
						}
					}
				}
				if calls != 1 || outputs != 1 {
					t.Errorf("round trip has %d calls and %d outputs, want one each", calls, outputs)
				}
				_, _ = fmt.Fprintf(w, "data: %s\n\n", completedResponseEvent("reply", 1, 1, 2))
			}))
			defer server.Close()
			provider := New(Config{BaseURL: server.URL})
			runner, err := coreagent.NewRunner(coreagent.RunnerConfig{
				Stream: provider.Stream,
				Model:  ai.Model{Name: contractModel},
				Tools: coreagent.ToolSet{"bash": func(_ context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
					executions.Add(1)
					if call.Arguments["command"] != "printf ready" {
						t.Errorf("executed arguments = %v", call.Arguments)
					}
					return []ai.ContentBlock{ai.TextContent{Text: "ready"}}, nil
				}},
				ToolDefinitions: []ai.ToolDefinition{{Name: "bash", InputSchema: map[string]any{"type": "object"}}},
			}, coreagent.WithCodeToolSurface(coreagent.CodeToolSurfaceBash))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.RunWithActiveStart(t.Context(), []ai.Message{ai.UserMessage{Content: "Check readiness."}}, 0, nil); err != nil {
				t.Fatal(err)
			}
			if executions.Load() != 1 || requests.Load() != 2 {
				t.Fatalf("executions=%d requests=%d, want one execution followed by a second model request", executions.Load(), requests.Load())
			}
		})
	}
}

func TestProviderRejectsStreamWithoutTerminalEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Let me finish that:\"}\n\n")
	}))
	defer server.Close()
	provider := New(Config{BaseURL: server.URL})
	runner, err := coreagent.NewRunner(coreagent.RunnerConfig{Stream: provider.Stream, Model: ai.Model{Name: contractModel}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunWithActiveStart(t.Context(), []ai.Message{ai.UserMessage{Content: "Do the work."}}, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "terminal event") {
		t.Fatalf("error = %v, want a missing-terminal error instead of successful completion", err)
	}
}
