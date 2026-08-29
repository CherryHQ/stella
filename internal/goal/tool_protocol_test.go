package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
)

// The split's whole promise is a contract with the model: it sees one exact
// schema per action, so an argument that belongs to a sibling action is refused
// before it reaches a handler. A union could not make that promise -- its schema
// was the union of every action's fields, and the lenient decoder dropped
// whatever did not fit.
//
// Goal tools are cold, so Code Mode is how the model reaches them: the schemas
// arrive through the code catalog. Assert them there, through the real loop
// with a fake provider, rather than by calling Execute directly.
func TestGoalToolsReachTheModelAsExactPerActionSchemas(t *testing.T) {
	h := newHarness(t)
	registry := goalToolRegistry(t, h)
	names := []string{"goal_cancel", "goal_create", "goal_get", "goal_list"}
	described := describeThroughCode(t, h, registry, names)

	for _, want := range names {
		definition, ok := described[want]
		if !ok {
			t.Fatalf("code catalog = %v, want %q among them", described, want)
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
	if required, _ := described["goal_get"].InputSchema["required"].([]any); len(required) != 1 || required[0] != "id" {
		t.Errorf("goal_get required = %v, want [id]", required)
	}
	if properties, _ := described["goal_list"].InputSchema["properties"].(map[string]any); properties["title"] != nil {
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
			messages := runGoalTurn(t, h, registry, invokeThroughCode(tc.tool, tc.arguments))
			if result := goalToolResult(t, messages); !strings.Contains(result, tc.want) {
				t.Fatalf("tool result = %q, want it to name %q", result, tc.want)
			}
		})
	}
}

// describedTool is the catalog entry the model reads before it invokes a cold
// tool through Code: the exact description and input schema, not the union's.
type describedTool struct {
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func describeThroughCode(t *testing.T, h *harness, registry *tools.Registry, names []string) map[string]describedTool {
	t.Helper()
	list, err := json.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf(`const out = {}; for (const name of %s) { out[name] = tools.describe(name); } return JSON.stringify(out);`, list)
	// The code result carries the script's return value, and that value is a
	// JSON string: unwrap it once before reading the catalog itself.
	result := goalToolResult(t, runGoalTurn(t, h, registry, codeCallStream(source)))
	var payload string
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("code result = %q: %v", result, err)
	}
	described := map[string]describedTool{}
	if err := json.Unmarshal([]byte(payload), &described); err != nil {
		t.Fatalf("code catalog = %q: %v", payload, err)
	}
	return described
}

// invokeThroughCode scripts the one call shape a cold tool now has: the model
// asks Code to invoke it, and a refusal comes back as the thrown error.
func invokeThroughCode(tool, arguments string) providers.StreamFunc {
	return codeCallStream(fmt.Sprintf(
		`try { return await tools.invoke(%q, %s); } catch (error) { return String(error); }`, tool, arguments))
}

func codeCallStream(source string) providers.StreamFunc {
	turns := 0
	return func(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
		turns++
		out := providers.NewChannelEventStream(4)
		go func() {
			if turns == 1 {
				raw, _ := json.Marshal(map[string]string{"code": source})
				out.Emit(ai.EventToolCallDelta{ID: "call-1", Name: coreagent.CodeToolName, Arguments: string(raw)})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventTextDelta{Text: "done"})
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		return out, nil
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
	})
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
