package memory_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
)

// splitTools builds the two generated memory tools over one Recall, the way
// cmd/stellad registers them.
func splitTools(t *testing.T, provider memory.Provider, private memory.RecallSource, group memory.GroupRecallSource) map[string]tools.Tool {
	t.Helper()
	recall := memory.NewRecall(provider, private, group)
	out := make(map[string]tools.Tool, len(memory.ActionTools()))
	for _, spec := range memory.ActionTools() {
		out[spec.Name] = memory.NewTool(recall, spec)
	}
	if len(out) != 2 {
		t.Fatalf("memory registers %d tools, want memory_search and memory_read", len(out))
	}
	return out
}

// The union hoisted every action's fields into one object and told the model in
// prose which combination was legal. Each split tool declares only its own, so
// the provider can validate the call before it is made.
func TestSplitMemoryToolsDeclareExactSealedSchemas(t *testing.T) {
	split := splitTools(t, memorytest.New(), &fakeRecallSource{}, nil)
	for name, want := range map[string][]string{
		"memory_search": {"q", "limit"},
		"memory_read":   {"ref", "token_cap"},
	} {
		schema := split[name].Definition().InputSchema
		if sealed, ok := schema["additionalProperties"].(bool); !ok || sealed {
			t.Errorf("%s declares additionalProperties=%v, want an explicit false", name, schema["additionalProperties"])
		}
		properties, _ := schema["properties"].(map[string]any)
		if properties["action"] != nil {
			t.Errorf("%s still declares the union's action property", name)
		}
		if len(properties) != len(want) {
			t.Errorf("%s properties = %v, want exactly %v", name, properties, want)
		}
		for _, property := range want {
			if properties[property] == nil {
				t.Errorf("%s omits %q", name, property)
			}
		}
		if strings.TrimSpace(split[name].Definition().Description) == "" {
			t.Errorf("%s has no description", name)
		}
	}
	// The union called free text `query`; the rule's name for it is `q` (§4).
	if properties, _ := split["memory_search"].Definition().InputSchema["properties"].(map[string]any); properties["query"] != nil {
		t.Error("memory_search still declares the union's query property")
	}
}

// Group routing is a security property, not a convenience: a group turn holds
// no private-memory authority, so it must reach the public lane on both tools
// and never resolve a private ref. Routing is by tool name now that no argument
// carries the action.
func TestSplitMemoryToolsRouteGroupTurnsByName(t *testing.T) {
	group := &fakeGroupRecallSource{rows: []memory.GroupRecallResult{{
		ID: "group-message-1", Seq: 4, ActorType: "human", ActorDisplayName: "Alice",
		Content: "older public detail", OccurredAt: time.Now().UTC(), Score: 1,
	}}}
	private := &fakeRecallSource{}
	split := splitTools(t, memorytest.New(), private, group)
	ctx := authz.WithAgentID(authz.WithGroupID(context.Background(), "group-1"), "agent-1")
	ctx = memory.WithGroupSeq(ctx, 9)
	ctx = memory.WithCurrentSpeaker(ctx, memory.CurrentSpeaker{UserID: "speaker-1"})

	out, err := split["memory_search"].Execute(ctx, map[string]any{"q": "older"})
	if err != nil || private.requestedSearchCap != 0 || group.searches != 1 || group.groupID != "group-1" || group.seq != 9 {
		t.Fatalf("group search output=%s err=%v private=%d group=%+v", out, err, private.requestedSearchCap, group)
	}
	if strings.Contains(out, "group-1") || strings.Contains(out, "actor_id") || !strings.Contains(out, `"authority": "information_only"`) {
		t.Fatalf("group search leaked internal provenance or lost authority: %s", out)
	}
	var search struct {
		Results []struct {
			Ref string `json:"ref"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &search); err != nil || len(search.Results) != 1 {
		t.Fatalf("decode group search: output=%s err=%v", out, err)
	}

	read, err := split["memory_read"].Execute(ctx, map[string]any{"ref": search.Results[0].Ref})
	if err != nil || private.requestedReadCap != 0 || group.reads != 1 ||
		!strings.Contains(read, "[seq:4 Alice]: older public detail") ||
		!strings.Contains(read, `"authority": "information_only"`) {
		t.Fatalf("group read output=%s err=%v private=%d group=%+v", read, err, private.requestedReadCap, group)
	}

	for _, ref := range []string{"profile", "soul", "constraints", "profile_versions", "soul_versions", "mem1.not-valid", "foreign-private-ref"} {
		if _, err := split["memory_read"].Execute(ctx, map[string]any{"ref": ref}); err == nil || err.Error() != "memory_read: ref not found" {
			t.Fatalf("group ref %q error=%v, want uniform not found", ref, err)
		}
	}
}

// The private lane keeps federating recall with durable memory, reached by name
// rather than by an action argument.
func TestSplitMemoryToolsRoutePrivateTurnsByName(t *testing.T) {
	fake := memorytest.New()
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "user-1"), "agent-1")
	if err := fake.SetProfile(ctx, "user-1", "agent-1", "Prefers jasmine tea"); err != nil {
		t.Fatal(err)
	}
	private := &fakeRecallSource{}
	split := splitTools(t, fake, private, &fakeGroupRecallSource{})

	out, err := split["memory_search"].Execute(ctx, map[string]any{"q": "jasmine"})
	if err != nil || !strings.Contains(out, "jasmine") {
		t.Fatalf("private search output=%s err=%v", out, err)
	}
	if private.requestedSearchCap == 0 {
		t.Fatal("private search never reached the conversation recall lane")
	}
	read, err := split["memory_read"].Execute(ctx, map[string]any{"ref": "profile"})
	if err != nil || !strings.Contains(read, "Prefers jasmine tea") {
		t.Fatalf("private read output=%s err=%v", read, err)
	}
}

// `q` is present but blank, so strict decoding lets it through and the handler
// rejects it. The message has to name `q`: a model told "query is required"
// retries with the union's field name and is refused again by the decoder.
func TestSplitMemorySearchNamesTheDeclaredFieldOnBlankInput(t *testing.T) {
	privateCtx := authz.WithAgentID(authz.WithUserID(context.Background(), "user-1"), "agent-1")
	groupCtx := memory.WithGroupSeq(authz.WithAgentID(authz.WithGroupID(context.Background(), "group-1"), "agent-1"), 9)

	for _, tc := range []struct {
		lane string
		ctx  context.Context
	}{
		{"private", privateCtx},
		{"group", groupCtx},
	} {
		t.Run(tc.lane, func(t *testing.T) {
			split := splitTools(t, memorytest.New(), &fakeRecallSource{}, &fakeGroupRecallSource{})
			for _, blank := range []string{"", "   ", "\t\n"} {
				_, err := split["memory_search"].Execute(tc.ctx, map[string]any{"q": blank})
				if err == nil || err.Error() != "memory_search: q is required" {
					t.Fatalf("blank q %q: err=%v, want memory_search: q is required", blank, err)
				}
			}
		})
	}
}

// A protocol test, not a behaviour claim: it asserts the definitions reach the
// provider and that a call the schema does not allow is refused before any
// handler runs. It says nothing about which tool a model would choose.
func TestSplitMemoryToolsReachTheProviderAndRefuseIllegalCalls(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tool      string
		arguments string
		want      string
	}{
		{"read without a ref", "memory_read", `{}`, "ref"},
		{"search without a query", "memory_search", `{}`, "q"},
		// `query` was the union's name for free text. On the split schema it is
		// simply an undeclared field, and strict decoding says so rather than
		// dropping it and searching for something else.
		{"the union's field name", "memory_search", `{"q":"older","query":"newer"}`, "query"},
		{"a field from the sibling tool", "memory_search", `{"q":"older","ref":"profile"}`, "ref"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tools.NewRegistry()
			recall := memory.NewRecall(memorytest.New(), &fakeRecallSource{}, nil)
			for _, spec := range memory.ActionTools() {
				if err := registry.Register(memory.NewTool(recall, spec)); err != nil {
					t.Fatalf("register %s: %v", spec.Name, err)
				}
			}

			var served []ai.ToolDefinition
			turns := 0
			stream := func(_ context.Context, _ ai.Model, request ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
				turns++
				if turns == 1 {
					served = request.Tools
				}
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
			runner, err := coreagent.NewRunner(coreagent.RunnerConfig{
				Stream:          stream,
				Tools:           coreagent.ToolSetFromRegistry(registry),
				ToolDefinitions: registry.Definitions(),
			}, coreagent.WithToolMode(coreagent.ToolModeNative))
			if err != nil {
				t.Fatalf("new runner: %v", err)
			}
			ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "user-1"), "agent-1")
			messages, err := runner.Continue(ctx, []ai.Message{ai.UserMessage{Content: "go"}}, func(coreagent.LoopEvent) {})
			if err != nil {
				t.Fatalf("run: %v", err)
			}

			byName := map[string]bool{}
			for _, definition := range served {
				byName[definition.Name] = true
			}
			for _, want := range memory.ToolNames() {
				if !byName[want] {
					t.Fatalf("provider tools = %v, want %q among them", served, want)
				}
			}
			if byName["memory"] {
				t.Fatal("the retired memory union still reaches the provider")
			}
			if result := splitToolResult(t, messages); !strings.Contains(result, tc.want) {
				t.Fatalf("tool result = %q, want it to name %q", result, tc.want)
			}
		})
	}
}

// splitToolResult returns the text of the single tool result in the transcript.
// A refused call still produces one: the loop hands the error back to the model
// instead of ending the turn.
func splitToolResult(t *testing.T, messages []ai.Message) string {
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
