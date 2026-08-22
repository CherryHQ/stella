package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

func TestToolModeProviderVisibility(t *testing.T) {
	definitions := []ai.ToolDefinition{{Name: "one"}, {Name: "two"}}
	toolSet := ToolSet{
		"one": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) { return nil, nil },
		"two": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) { return nil, nil },
	}
	for _, tt := range []struct {
		name string
		mode ToolMode
		want []string
	}{
		{name: "native", mode: ToolModeNative, want: []string{"one", "two"}},
		{name: "code", mode: ToolModeCode, want: []string{codeToolName}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var seen ai.Context
			stream := func(_ context.Context, _ ai.Model, request ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
				seen = request
				out := providers.NewChannelEventStream(2)
				go func() {
					out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
					out.Finish(nil)
				}()
				return out, nil
			}
			runner, err := NewRunner(RunnerConfig{Stream: stream, Tools: toolSet, ToolDefinitions: definitions}, WithToolMode(tt.mode))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil); err != nil {
				t.Fatal(err)
			}
			if len(seen.Tools) != len(tt.want) {
				t.Fatalf("provider tools = %#v, want %v", seen.Tools, tt.want)
			}
			for i, name := range tt.want {
				if seen.Tools[i].Name != name {
					t.Fatalf("provider tool %d = %q, want %q", i, seen.Tools[i].Name, name)
				}
			}
		})
	}
}

type codeTestKey string

type codePhaseHook struct {
	definitions []ai.ToolDefinition
	providerCtx context.Context
	preToolCtx  context.Context
	postCalls   int
}

func (codePhaseHook) Name() string  { return "code-phase" }
func (codePhaseHook) Priority() int { return 0 }

func (h *codePhaseHook) OnPreLLMCall(context.Context, *hooks.PreLLMCallContext) (hooks.PreLLMCallResult, error) {
	return hooks.PreLLMCallResult{ToolDefinitions: h.definitions, Context: h.providerCtx}, nil
}

func (h *codePhaseHook) OnPreToolCall(ctx context.Context, _ *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
	h.preToolCtx = ctx
	return hooks.PreToolCallResult{Context: context.WithValue(ctx, codeTestKey("child"), "enriched")}, nil
}

func (h *codePhaseHook) OnPostToolCall(context.Context, *hooks.PostToolCallContext) { h.postCalls++ }

type codeSequenceHook struct{ order *[]string }

func (codeSequenceHook) Name() string  { return "code-sequence" }
func (codeSequenceHook) Priority() int { return 0 }
func (h codeSequenceHook) OnPreToolCall(context.Context, *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
	*h.order = append(*h.order, "pre")
	return hooks.PreToolCallResult{}, nil
}

func (h codeSequenceHook) OnPostToolCall(context.Context, *hooks.PostToolCallContext) {
	*h.order = append(*h.order, "post")
}

func TestCodeStrategyUsesEffectiveSnapshotAndSharedChildCore(t *testing.T) {
	ref := renderrefs.Reference{V: 1, Type: "task", ID: "task-1"}
	var sentinel strings.Builder
	if err := renderrefs.Emit(&sentinel, ref); err != nil {
		t.Fatal(err)
	}
	outerCtx := context.WithValue(context.Background(), codeTestKey("outer"), "outer")
	providerCtx := context.WithValue(outerCtx, codeTestKey("provider"), "provider")
	hook := &codePhaseHook{definitions: []ai.ToolDefinition{{Name: "visible", Description: "visible tool", InputSchema: map[string]any{"type": "object"}}}, providerCtx: providerCtx}
	var toolCalls []ai.ToolCall
	visible := func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
		if got := ctx.Value(codeTestKey("child")); got != "enriched" {
			t.Fatalf("child context = %v, want PreToolCall enrichment", got)
		}
		toolCalls = append(toolCalls, call)
		return []ai.ContentBlock{
			ai.TextContent{Text: "first\n" + sentinel.String()},
			ai.TextContent{Text: "second\n" + sentinel.String()},
		}, nil
	}
	providerCalls := 0
	stream := func(ctx context.Context, _ ai.Model, request ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		if ctx.Value(codeTestKey("provider")) != "provider" {
			t.Fatal("PreLLM context was not passed to provider stream")
		}
		if len(request.Tools) != 1 || request.Tools[0].Name != codeToolName {
			t.Fatalf("code provider tools = %#v", request.Tools)
		}
		providerCalls++
		out := providers.NewChannelEventStream(4)
		go func() {
			if providerCalls == 1 {
				out.Emit(ai.EventToolCallDelta{ID: "outer", Name: codeToolName, Arguments: `{"code":"const names = tools.search('').map(t => t.name); const description = tools.describe('visible'); const value = await tools.invoke('visible', {q: 1}); return {names, description, value};"}`})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventTextDelta{Text: "done"})
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		return out, nil
	}
	runner, err := NewRunner(RunnerConfig{
		Stream: stream,
		Tools: ToolSet{
			"visible": visible,
			"hidden": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
				t.Fatal("hidden tool executed")
				return nil, nil
			},
		},
		ToolDefinitions: []ai.ToolDefinition{{Name: "visible"}, {Name: "hidden"}},
	}, WithToolMode(ToolModeCode), WithHooks(hooks.NewHookSet([]hooks.HookPlugin{hook}), hooks.HookMeta{}))
	if err != nil {
		t.Fatal(err)
	}
	var events []LoopEvent
	history, err := runner.RunWithActiveStart(outerCtx, []ai.Message{ai.UserMessage{Content: "go"}}, 0, func(event LoopEvent) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if hook.preToolCtx.Value(codeTestKey("provider")) != nil {
		t.Fatal("PreLLM context leaked into child tool execution")
	}
	if len(toolCalls) != 1 || toolCalls[0].ID != "outer:1" {
		t.Fatalf("child calls = %#v, want child ID outer:1", toolCalls)
	}
	if hook.postCalls != 1 {
		t.Fatalf("PostToolCall count = %d, want 1", hook.postCalls)
	}
	if len(history) != 4 {
		t.Fatalf("history length = %d, want user + outer call/result + final assistant", len(history))
	}
	outerResult, ok := history[2].(ai.ToolResultMessage)
	if !ok || outerResult.ToolCallID != "outer" || outerResult.ToolName != codeToolName {
		t.Fatalf("outer result = %#v", history[2])
	}
	if len(outerResult.References) != 1 || outerResult.References[0].ID != ref.ID {
		t.Fatalf("outer references = %#v", outerResult.References)
	}
	if strings.Contains(ai.FlattenText(outerResult.Content), "::stella-ref/") {
		t.Fatalf("renderref leaked into VM/outer result: %q", ai.FlattenText(outerResult.Content))
	}
	for _, event := range events {
		if started, ok := event.(ToolStarted); ok && started.ToolCall.ID != "outer" {
			t.Fatalf("child ToolStarted leaked: %#v", started)
		}
		if finished, ok := event.(ToolFinished); ok && finished.Result.ToolCallID != "outer" {
			t.Fatalf("child ToolFinished leaked: %#v", finished)
		}
	}
}

func TestCodeStrategyFailsClosedAndMapsOuterResults(t *testing.T) {
	tools := ToolSet{
		"good": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
		},
		"bad": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) { return nil, errors.New("child failed") },
		"hidden": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			t.Fatal("hidden tool executed")
			return nil, nil
		},
	}
	defs := []ai.ToolDefinition{{Name: "good"}, {Name: "bad"}}
	for _, tt := range []struct {
		name      string
		call      ai.ToolCall
		wantError bool
		wantText  string
	}{
		{name: "forged native", call: ai.ToolCall{ID: "outer", Name: "hidden"}, wantError: true, wantText: "tool not found"},
		{name: "direct ToolValue", call: ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `return {blocks:[{type:"text",text:"direct"}],isError:true};`}}, wantError: true, wantText: "direct"},
		{name: "caught child error", call: ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `try { await tools.invoke("bad"); } catch (error) { return error; }`}}, wantError: true, wantText: "child failed"},
		{name: "child then uncaught error", call: ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `await tools.invoke("good"); throw new Error("after child");`}}, wantError: true, wantText: "after child"},
		{name: "uncaught execution error", call: ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `throw new Error("boom");`}}, wantError: true, wantText: "boom"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			results, err := executeCodeCalls(context.Background(), []ai.ToolCall{tt.call}, tools, defs, toolCallbacks{}, nil, hooks.HookMeta{}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].IsError != tt.wantError || !strings.Contains(ai.FlattenText(results[0].Content), tt.wantText) {
				t.Fatalf("result = %#v", results)
			}
		})
	}
}

func TestEffectiveSnapshotFailsClosedForNativeAndCatalog(t *testing.T) {
	hook := &codePhaseHook{definitions: []ai.ToolDefinition{{Name: "visible"}, {Name: "added-without-handler"}}}
	toolSet := ToolSet{
		"visible": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.TextContent{Text: "visible"}}, nil
		},
		"hidden": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			t.Fatal("hidden tool executed")
			return nil, nil
		},
	}
	t.Run("native forged call", func(t *testing.T) {
		stream := toolCallThenStopStream(ai.ToolCall{ID: "forged", Name: "hidden"})
		runner, err := NewRunner(RunnerConfig{Stream: stream, Tools: toolSet, ToolDefinitions: []ai.ToolDefinition{{Name: "visible"}, {Name: "hidden"}}}, WithHooks(hooks.NewHookSet([]hooks.HookPlugin{hook}), hooks.HookMeta{}))
		if err != nil {
			t.Fatal(err)
		}
		history, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		result := history[2].(ai.ToolResultMessage)
		if !result.IsError || ai.FlattenText(result.Content) != "tool not found" {
			t.Fatalf("forged native result = %#v", result)
		}
	})
	t.Run("code catalog and invoke", func(t *testing.T) {
		stream := toolCallThenStopStream(ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `
const names = tools.search("").map(tool => tool.name);
let hiddenDescribe; try { tools.describe("hidden"); } catch (_) { hiddenDescribe = true; }
let hiddenInvoke; try { await tools.invoke("hidden"); } catch (_) { hiddenInvoke = true; }
let addedInvoke; try { await tools.invoke("added-without-handler"); } catch (_) { addedInvoke = true; }
return { names, hiddenDescribe, hiddenInvoke, addedInvoke };
`}})
		runner, err := NewRunner(RunnerConfig{Stream: stream, Tools: toolSet, ToolDefinitions: []ai.ToolDefinition{{Name: "visible"}, {Name: "hidden"}}}, WithToolMode(ToolModeCode), WithHooks(hooks.NewHookSet([]hooks.HookPlugin{hook}), hooks.HookMeta{}))
		if err != nil {
			t.Fatal(err)
		}
		history, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Names          []string `json:"names"`
			HiddenDescribe bool     `json:"hiddenDescribe"`
			HiddenInvoke   bool     `json:"hiddenInvoke"`
			AddedInvoke    bool     `json:"addedInvoke"`
		}
		if err := json.Unmarshal([]byte(ai.FlattenText(history[2].(ai.ToolResultMessage).Content)), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Names) != 1 || got.Names[0] != "visible" || !got.HiddenDescribe || !got.HiddenInvoke || !got.AddedInvoke {
			t.Fatalf("effective catalog result = %#v", got)
		}
	})
}

func TestCodeChildUsesNativeLifecycleOrderWithoutChildCallbacks(t *testing.T) {
	var order []string
	callbacks := toolCallbacks{
		onStart:  func(ai.ToolCall) { order = append(order, "outer-start") },
		onFinish: func(ai.ToolResultMessage) { order = append(order, "outer-finish") },
	}
	lifecycle := &ToolLifecycle{
		BeforeCall: func(context.Context, ToolCallContext) (ToolCallMutation, error) {
			order = append(order, "before")
			return ToolCallMutation{}, nil
		},
		AfterCall: func(context.Context, ToolResultContext) (ToolResultMutation, error) {
			order = append(order, "after")
			return ToolResultMutation{}, nil
		},
	}
	hook := codeSequenceHook{order: &order}
	canonicalize := func(_ context.Context, result ai.ToolResultMessage) (ai.ToolResultMessage, error) {
		order = append(order, "canonicalize")
		return result, nil
	}
	results, err := executeCodeCalls(context.Background(), []ai.ToolCall{{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `return await tools.invoke("echo");`}}}, ToolSet{
		"echo": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			order = append(order, "tool")
			return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
		},
	}, []ai.ToolDefinition{{Name: "echo"}}, callbacks, hooks.NewHookSet([]hooks.HookPlugin{hook}), hooks.HookMeta{}, lifecycle, canonicalize)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("results = %#v", results)
	}
	if got, want := strings.Join(order, ","), "outer-start,before,pre,tool,after,post,canonicalize,outer-finish"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

func toolCallThenStopStream(call ai.ToolCall) providers.StreamFunc {
	invocations := 0
	return func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		invocations++
		out := providers.NewChannelEventStream(4)
		go func() {
			if invocations == 1 {
				raw, _ := json.Marshal(call.Arguments)
				out.Emit(ai.EventToolCallDelta{ID: call.ID, Name: call.Name, Arguments: string(raw)})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		return out, nil
	}
}

func TestCodeResultJSONRoundTrip(t *testing.T) {
	result := codeResultFromJSON(ai.ToolResultMessage{ToolCallID: "outer", ToolName: codeToolName}, json.RawMessage(`{"answer":42}`))
	if got, want := ai.FlattenText(result.Content), `{"answer":42}`; got != want || result.IsError {
		t.Fatalf("result = %#v", result)
	}
}
