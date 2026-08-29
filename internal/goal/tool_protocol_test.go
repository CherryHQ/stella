package goal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
)

// The split's whole promise is a contract with the provider: the model sees one
// exact schema per action, so an argument that belongs to a sibling action is
// refused before it reaches a handler. A union could not make that promise —
// its schema was the union of every action's fields, and the lenient decoder
// dropped whatever did not fit.
//
// Assert it through the real loop with a fake provider rather than by calling
// Execute directly: the schemas the provider receives are the thing under test.
func TestGoalToolsReachTheProviderAsExactPerActionSchemas(t *testing.T) {
	h := newHarness(t)
	registry := goalToolRegistry(t, h)

	var served []ai.ToolDefinition
	stream := func(_ context.Context, _ ai.Model, request ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		served = request.Tools
		out := providers.NewChannelEventStream(2)
		go func() {
			out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			out.Finish(nil)
		}()
		return out, nil
	}
	runGoalTurn(t, h, registry, stream)

	names := make([]string, 0, len(served))
	byName := map[string]ai.ToolDefinition{}
	for _, definition := range served {
		names = append(names, definition.Name)
		byName[definition.Name] = definition
	}
	for _, want := range []string{"goal_cancel", "goal_create", "goal_get", "goal_list"} {
		definition, ok := byName[want]
		if !ok {
			t.Fatalf("provider tools = %v, want %q among them", names, want)
		}
		if sealed, _ := definition.InputSchema["additionalProperties"].(bool); sealed {
			t.Errorf("%s schema accepts extra properties, want a sealed schema", want)
		}
		if definition.Description == "" {
			t.Errorf("%s has no description", want)
		}
	}
	// goal_get takes an id and nothing else; goal_list never took a title. Under
	// the union both fields were siblings on one flat schema.
	if required, _ := byName["goal_get"].InputSchema["required"].([]any); len(required) != 1 || required[0] != "id" {
		t.Errorf("goal_get required = %v, want [id]", required)
	}
	if properties, _ := byName["goal_list"].InputSchema["properties"].(map[string]any); properties["title"] != nil {
		t.Errorf("goal_list declares a title property, which belongs to goal_create")
	}
}

func TestGoalToolsRefuseArgumentsThatBelongToAnotherAction(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tool      string
		arguments string
		want      string
	}{
		{"missing required id", "goal_get", `{}`, "id"},
		{"field from a sibling action", "goal_list", `{"title":"ship it"}`, "title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			registry := goalToolRegistry(t, h)

			turns := 0
			stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
				turns++
				out := providers.NewChannelEventStream(4)
				go func() {
					if turns == 1 {
						out.Emit(ai.EventToolCallDelta{ID: "call-1", Name: tc.tool, Arguments: tc.arguments})
						out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
					} else {
						out.Emit(ai.EventTextDelta{Text: "done"})
						out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
					}
					out.Finish(nil)
				}()
				return out, nil
			}

			result := goalToolResult(t, runGoalTurn(t, h, registry, stream))
			if !strings.Contains(result, tc.want) {
				t.Fatalf("tool result = %q, want it to name %q", result, tc.want)
			}
		})
	}
}

func goalToolRegistry(t *testing.T, h *harness) *tools.Registry {
	t.Helper()
	registry := tools.NewRegistry()
	for _, spec := range ActionTools() {
		if err := registry.Register(NewTool(h.bundle, spec)); err != nil {
			t.Fatalf("register %s: %v", spec.Name, err)
		}
	}
	return registry
}

func runGoalTurn(t *testing.T, h *harness, registry *tools.Registry, stream providers.StreamFunc) []ai.Message {
	t.Helper()
	runner, err := coreagent.NewRunner(coreagent.RunnerConfig{
		Stream:          stream,
		Tools:           coreagent.ToolSetFromRegistry(registry),
		ToolDefinitions: registry.Definitions(),
	}, coreagent.WithToolMode(coreagent.ToolModeNative))
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), h.userID), h.agentID)
	messages, err := runner.Continue(ctx, []ai.Message{ai.UserMessage{Content: "go"}}, func(coreagent.LoopEvent) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return messages
}

// goalToolResult returns the text of the single tool result in the transcript.
// A refused call still produces one: the loop hands the error back to the model
// instead of ending the turn.
func goalToolResult(t *testing.T, messages []ai.Message) string {
	t.Helper()
	for _, message := range messages {
		result, ok := message.(ai.ToolResultMessage)
		if !ok {
			continue
		}
		var parts []string
		for _, block := range result.Content {
			if text, ok := block.(ai.TextContent); ok {
				parts = append(parts, text.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	raw, _ := json.Marshal(messages)
	t.Fatalf("no tool result in transcript: %s", raw)
	return ""
}
