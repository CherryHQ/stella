package access

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
)

// The split's whole promise is a contract with the provider: the model sees one
// exact schema per action, so a call that omits the field its action requires is
// refused before it reaches a handler. The union could only say so in prose —
// session_id was optional on its schema because three of its four actions did
// not take one.
//
// Assert it through the real loop with a fake provider rather than by calling
// Execute directly: the schemas the provider receives are the thing under test.
func TestSessionToolsReachTheProviderAsExactPerActionSchemas(t *testing.T) {
	m := newSessionMatrix(t)

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
	runSessionTurn(t, m, stream)

	names := make([]string, 0, len(served))
	byName := map[string]ai.ToolDefinition{}
	for _, definition := range served {
		names = append(names, definition.Name)
		byName[definition.Name] = definition
	}
	for _, want := range ToolNames() {
		definition, ok := byName[want]
		if !ok {
			t.Fatalf("provider tools = %v, want %q among them", names, want)
		}
		if sealed, _ := definition.InputSchema["additionalProperties"].(bool); sealed {
			t.Errorf("%s schema accepts extra properties, want a sealed schema", want)
		}
		if properties, _ := definition.InputSchema["properties"].(map[string]any); properties["action"] != nil {
			t.Errorf("%s still declares the union's action property", want)
		}
		if definition.Description == "" {
			t.Errorf("%s has no description", want)
		}
	}
	// session_send is the reason the union's schema could not be exact: it needs
	// both fields, while session_list takes neither.
	if required, _ := byName["session_send"].InputSchema["required"].([]any); len(required) != 2 {
		t.Errorf("session_send required = %v, want both message and session_id", required)
	}
	if properties, _ := byName[ListTool].InputSchema["properties"].(map[string]any); properties["session_id"] != nil {
		t.Errorf("%s declares a session_id property, which belongs to the addressed actions", ListTool)
	}
}

func TestSessionToolsRefuseCallsMissingTheirRequiredField(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tool      string
		arguments string
		want      string
	}{
		{"send without a target", "session_send", `{"message":"continue"}`, "session_id"},
		{"get without a target", "session_get", `{}`, "session_id"},
		{"field from a sibling action", ListTool, `{"session_id":"private"}`, "session_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newSessionMatrix(t)
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

			result := sessionToolResult(t, runSessionTurn(t, m, stream))
			if !strings.Contains(result, tc.want) {
				t.Fatalf("tool result = %q, want it to name %q", result, tc.want)
			}
		})
	}
}

func runSessionTurn(t *testing.T, m sessionMatrix, stream providers.StreamFunc) []ai.Message {
	t.Helper()
	registry := tools.NewRegistry()
	for _, spec := range ActionTools() {
		registry.Register(NewTool(m.svc, spec))
	}
	runner, err := coreagent.NewRunner(coreagent.RunnerConfig{
		Stream:          stream,
		Tools:           coreagent.ToolSetFromRegistry(registry),
		ToolDefinitions: registry.Definitions(),
	}, coreagent.WithToolMode(coreagent.ToolModeNative))
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	ctx := memory.WithSessionID(authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent), "source-session")
	messages, err := runner.Continue(ctx, []ai.Message{ai.UserMessage{Content: "go"}}, func(coreagent.LoopEvent) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return messages
}

// sessionToolResult returns the text of the single tool result in the
// transcript. A refused call still produces one: the loop hands the error back
// to the model instead of ending the turn.
func sessionToolResult(t *testing.T, messages []ai.Message) string {
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
