package access

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
)

// The split's whole promise is a contract with the model: it sees one exact
// schema per action, so a call that omits the field its action requires is
// refused before it reaches a handler. The union could only say so in prose --
// session_id was optional on its schema because three of its four actions did
// not take one.
//
// Code Mode is the only mode, so these schemas reach the model through the code
// tool's catalog rather than the provider's tool list. Assert them there,
// through the real loop, rather than by calling Execute directly.
func TestSessionToolsReachTheModelAsExactPerActionSchemas(t *testing.T) {
	m := newSessionMatrix(t)
	described := describeThroughCode(t, ToolNames(), func(stream providers.StreamFunc) []ai.Message {
		return runSessionTurn(t, m, stream)
	})

	for _, want := range ToolNames() {
		definition, ok := described[want]
		if !ok {
			t.Fatalf("catalog = %v, want %q among them", described, want)
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
	if required, _ := described["session_send"].InputSchema["required"].([]any); len(required) != 2 {
		t.Errorf("session_send required = %v, want both message and session_id", required)
	}
	if properties, _ := described[ListTool].InputSchema["properties"].(map[string]any); properties["session_id"] != nil {
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
			result := sessionToolResult(t, runSessionTurn(t, m, invokeThroughCode(tc.tool, tc.arguments)))
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
		if err := registry.Register(NewTool(m.svc, spec)); err != nil {
			t.Fatalf("register %s: %v", spec.Name, err)
		}
	}
	runner, err := coreagent.NewRunner(coreagent.RunnerConfig{
		Stream:          stream,
		Tools:           coreagent.ToolSetFromRegistry(registry),
		ToolDefinitions: registry.Definitions(),
	})
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

// describedTool is the catalog entry the model reads before it invokes a tool
// through Code: the exact description and input schema, not the union's.
type describedTool struct {
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// describeThroughCode runs one turn whose single code call describes each name
// and returns the catalog as JSON, which is how a cold tool's schema reaches the
// model now that Code Mode is the only mode.
func describeThroughCode(t *testing.T, names []string, run func(providers.StreamFunc) []ai.Message) map[string]describedTool {
	t.Helper()
	list, err := json.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf(`const out = {}; for (const name of %s) { out[name] = tools.describe(name); } return JSON.stringify(out);`, list)
	// The code result carries the script's return value, and that value is a
	// JSON string: unwrap it once before reading the catalog itself.
	result := sessionToolResult(t, run(codeCallStream(source)))
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
