package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/codemode"
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

func TestCodeModeProviderJourneyUsesOneSchemaAcrossAdapters(t *testing.T) {
	for _, api := range []string{"openai", "anthropic", "openai-response"} {
		t.Run(api, func(t *testing.T) {
			calls := 0
			stream := func(_ context.Context, model ai.Model, request ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
				if model.API != api {
					t.Fatalf("provider API = %q, want %q", model.API, api)
				}
				if len(request.Tools) != 1 || request.Tools[0].Name != codeToolName {
					t.Fatalf("provider tools = %#v, want one code schema", request.Tools)
				}
				calls++
				out := providers.NewChannelEventStream(3)
				go func() {
					if calls == 1 {
						raw, _ := json.Marshal(map[string]string{"code": `
const found = tools.search("echo");
const described = tools.describe(found[0].name);
return await tools.invoke(described.name, { value: "ok" });`})
						out.Emit(ai.EventToolCallDelta{ID: "outer", Name: codeToolName, Arguments: string(raw)})
						out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
					} else {
						out.Emit(ai.EventTextDelta{Text: "final"})
						out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
					}
					out.Finish(nil)
				}()
				return out, nil
			}
			runner, err := NewRunner(RunnerConfig{
				Stream: stream,
				Model:  ai.Model{ID: "fake", API: api},
				Tools: ToolSet{"echo": func(_ context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
					return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprint(call.Arguments["value"])}}, nil
				}},
				ToolDefinitions: []ai.ToolDefinition{{Name: "echo", Description: "echo a value"}},
			}, WithToolMode(ToolModeCode))
			if err != nil {
				t.Fatal(err)
			}
			history, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil)
			if err != nil || calls != 2 || len(history) == 0 {
				t.Fatalf("provider journey err=%v calls=%d history=%#v", err, calls, history)
			}
		})
	}
}

func TestCodeModeHidesSyntheticToolForExplicitEmptyHookCatalog(t *testing.T) {
	var seen ai.Context
	stream := func(_ context.Context, _ ai.Model, request ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		seen = request
		out := providers.NewChannelEventStream(1)
		go func() {
			out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			out.Finish(nil)
		}()
		return out, nil
	}
	hook := &codePhaseHook{definitions: []ai.ToolDefinition{}}
	runner, err := NewRunner(RunnerConfig{
		Stream:          stream,
		Tools:           ToolSet{"visible": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) { return nil, nil }},
		ToolDefinitions: []ai.ToolDefinition{{Name: "visible"}},
	}, WithToolMode(ToolModeCode), WithHooks(hooks.NewHookSet([]hooks.HookPlugin{hook}), hooks.HookMeta{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil); err != nil {
		t.Fatal(err)
	}
	if len(seen.Tools) != 0 {
		t.Fatalf("explicit empty hook catalog exposed provider tools: %#v", seen.Tools)
	}
}

func TestCodeModeRejectsForgedCodeForExplicitEmptyHookCatalog(t *testing.T) {
	providerCalls := 0
	stream := func(_ context.Context, _ ai.Model, request ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		if len(request.Tools) != 0 {
			t.Fatalf("explicit empty hook catalog exposed provider tools: %#v", request.Tools)
		}
		providerCalls++
		out := providers.NewChannelEventStream(2)
		go func() {
			if providerCalls == 1 {
				out.Emit(ai.EventToolCallDelta{ID: "forged", Name: codeToolName, Arguments: `{"code":"throw new Error('source ran')"}`})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		return out, nil
	}
	hook := &codePhaseHook{definitions: []ai.ToolDefinition{}}
	runner, err := NewRunner(RunnerConfig{
		Stream: stream,
		Tools: ToolSet{"visible": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			t.Fatal("forged code source executed a child tool")
			return nil, nil
		}},
		ToolDefinitions: []ai.ToolDefinition{{Name: "visible"}},
	}, WithToolMode(ToolModeCode), WithHooks(hooks.NewHookSet([]hooks.HookPlugin{hook}), hooks.HookMeta{}))
	if err != nil {
		t.Fatal(err)
	}
	history, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := history[2].(ai.ToolResultMessage)
	if !ok || !result.IsError || ai.FlattenText(result.Content) != "tool not available" {
		t.Fatalf("forged empty-catalog code result = %#v", history)
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
		{name: "direct ToolValue", call: ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `return {kind:"stella.tool_value",version:1,blocks:[{type:"text",text:"direct"}],isError:true};`}}, wantError: true, wantText: "direct"},
		{name: "caught child error", call: ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `try { await tools.invoke("bad"); } catch (error) { return error.value; }`}}, wantError: true, wantText: "child failed"},
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

func TestCodeStrategyRejectsBlockedChildren(t *testing.T) {
	for _, tt := range []struct {
		name      string
		want      string
		hooks     *hooks.HookSet
		lifecycle *ToolLifecycle
	}{
		{
			name: "lifecycle block",
			want: "lifecycle block",
			lifecycle: &ToolLifecycle{BeforeCall: func(context.Context, ToolCallContext) (ToolCallMutation, error) {
				return ToolCallMutation{Block: true, BlockMessage: "lifecycle block"}, nil
			}},
		},
		{
			name: "hook block",
			want: "hook block",
			hooks: hooks.NewHookSet([]hooks.HookPlugin{toolExecutionHook{
				pre: func(context.Context, *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
					return hooks.PreToolCallResult{Block: true, BlockMessage: "hook block"}, nil
				},
				post: func(context.Context, *hooks.PostToolCallContext) {},
			}}),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
				"code": `try { await tools.invoke("blocked"); return "resolved"; } catch (error) { return error.value.blocks[0].text; }`,
			}}, ToolSet{
				"blocked": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
					t.Fatal("blocked child should not execute")
					return nil, nil
				},
			}, []ai.ToolDefinition{{Name: "blocked"}}, tt.hooks, hooks.HookMeta{}, tt.lifecycle, nil)
			if result.IsError || !strings.Contains(ai.FlattenText(result.Content), tt.want) {
				t.Fatalf("caught blocked child = %#v", result)
			}
		})
	}
}

func TestCodeStrategyFailedOuterCallRetainsChildEffects(t *testing.T) {
	ref := renderrefs.Reference{V: 1, Type: "task", ID: "committed"}
	var sentinel strings.Builder
	if err := renderrefs.Emit(&sentinel, ref); err != nil {
		t.Fatal(err)
	}
	tools := ToolSet{"effect": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		return []ai.ContentBlock{ai.TextContent{Text: "created\n" + sentinel.String()}}, nil
	}}
	call := func(source string) ai.ToolCall {
		return ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": source}}
	}
	assertFailedEffect := func(t *testing.T, result ai.ToolResultMessage) {
		t.Helper()
		if !result.IsError || len(result.References) != 1 || result.References[0].ID != ref.ID {
			t.Fatalf("failed side-effect result = %#v", result)
		}
		details, ok := result.Details.(codeExecutionDetails)
		if !ok || !details.ChildSideEffectsMayHaveCommitted {
			t.Fatalf("side-effect details = %#v", result.Details)
		}
		if !strings.Contains(ai.FlattenText(result.Content), childEffectNotice) {
			t.Fatalf("missing retry warning: %q", ai.FlattenText(result.Content))
		}
	}

	t.Run("throw", func(t *testing.T) {
		assertFailedEffect(t, executeCodeCall(context.Background(), call(`await tools.invoke("effect"); throw new Error("after effect");`), tools, []ai.ToolDefinition{{Name: "effect"}}, nil, hooks.HookMeta{}, nil, nil))
	})
	t.Run("limit", func(t *testing.T) {
		result := executeCodeCallWithLimits(context.Background(), call(`await tools.invoke("effect"); while (true) {}`), tools, []ai.ToolDefinition{{Name: "effect"}}, nil, hooks.HookMeta{}, nil, nil, codemode.Limits{WallClock: 25 * time.Millisecond})
		assertFailedEffect(t, result)
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		called := make(chan struct{})
		cancelTools := ToolSet{"effect": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			close(called)
			return []ai.ContentBlock{ai.TextContent{Text: "created\n" + sentinel.String()}}, nil
		}}
		resultCh := make(chan ai.ToolResultMessage, 1)
		go func() {
			resultCh <- executeCodeCall(ctx, call(`await tools.invoke("effect"); while (true) {}`), cancelTools, []ai.ToolDefinition{{Name: "effect"}}, nil, hooks.HookMeta{}, nil, nil)
		}()
		select {
		case <-called:
			cancel()
		case <-time.After(time.Second):
			t.Fatal("child was not invoked")
		}
		select {
		case result := <-resultCh:
			assertFailedEffect(t, result)
		case <-time.After(time.Second):
			t.Fatal("cancelled code call did not return")
		}
	})
}

func TestCodeStrategyInfrastructureFailureCannotBeCaught(t *testing.T) {
	for _, tt := range []struct {
		name         string
		want         string
		content      []ai.ContentBlock
		lifecycle    *ToolLifecycle
		canonicalize ToolImageCanonicalizer
	}{
		{
			name: "before lifecycle",
			want: "lifecycle unavailable",
			lifecycle: &ToolLifecycle{BeforeCall: func(context.Context, ToolCallContext) (ToolCallMutation, error) {
				return ToolCallMutation{}, errors.New("lifecycle unavailable")
			}},
		},
		{
			name: "after lifecycle",
			want: "after lifecycle unavailable",
			lifecycle: &ToolLifecycle{AfterCall: func(context.Context, ToolResultContext) (ToolResultMutation, error) {
				return ToolResultMutation{}, errors.New("after lifecycle unavailable")
			}},
		},
		{
			name: "canonicalizer",
			want: "canonicalizer unavailable",
			canonicalize: func(context.Context, ai.ToolResultMessage) (ai.ToolResultMessage, error) {
				return ai.ToolResultMessage{}, errors.New("canonicalizer unavailable")
			},
		},
		{
			name:    "bridge core",
			want:    "code bridge rejects unsupported",
			content: []ai.ContentBlock{ai.ImageContent{Data: "raw", MimeType: "image/png"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			content := tt.content
			if content == nil {
				content = []ai.ContentBlock{ai.TextContent{Text: "ok"}}
			}
			result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
				"code": `try { await tools.invoke("effect"); return "swallowed"; } catch (_) { return "still swallowed"; }`,
			}}, ToolSet{
				"effect": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) { return content, nil },
			}, []ai.ToolDefinition{{Name: "effect"}}, nil, hooks.HookMeta{}, tt.lifecycle, tt.canonicalize)
			if !result.IsError || !strings.Contains(ai.FlattenText(result.Content), tt.want) || strings.Contains(ai.FlattenText(result.Content), "swallowed") {
				t.Fatalf("infrastructure failure was catchable: %#v", result)
			}
		})
	}
}

func TestCodeStrategyDrainsUnawaitedChildResults(t *testing.T) {
	ref := renderrefs.Reference{V: 1, Type: "task", ID: "unawaited"}
	var sentinel strings.Builder
	if err := renderrefs.Emit(&sentinel, ref); err != nil {
		t.Fatal(err)
	}
	t.Run("reference", func(t *testing.T) {
		result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `tools.invoke("effect"); return "ok";`}}, ToolSet{
			"effect": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
				return []ai.ContentBlock{ai.TextContent{Text: "created\n" + sentinel.String()}}, nil
			},
		}, []ai.ToolDefinition{{Name: "effect"}}, nil, hooks.HookMeta{}, nil, nil)
		if result.IsError || len(result.References) != 1 || result.References[0].ID != ref.ID {
			t.Fatalf("unawaited reference result = %#v", result)
		}
	})
	t.Run("business failure", func(t *testing.T) {
		result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `tools.invoke("effect"); return "ok";`}}, ToolSet{
			"effect": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
				return nil, errors.New("unawaited side effect failed")
			},
		}, []ai.ToolDefinition{{Name: "effect"}}, nil, hooks.HookMeta{}, nil, nil)
		if !result.IsError || !strings.Contains(ai.FlattenText(result.Content), "unawaited side effect failed") {
			t.Fatalf("unawaited failure result = %#v", result)
		}
	})
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

func TestCodeCatalogSearchCapsEmptyQuery(t *testing.T) {
	definitions := make([]ai.ToolDefinition, 25)
	for i := range definitions {
		definitions[i] = ai.ToolDefinition{Name: fmt.Sprintf("tool-%02d", i)}
	}
	result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `return tools.search("");`}}, ToolSet{}, definitions, nil, hooks.HookMeta{}, nil, nil)
	var got []struct{ Name string }
	if err := json.Unmarshal([]byte(ai.FlattenText(result.Content)), &got); err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(got) != 20 || got[0].Name != "tool-00" || got[19].Name != "tool-19" {
		t.Fatalf("empty search = %#v", result)
	}
}

func TestCodeToolValueEnvelopeCollisionsRemainOrdinaryJSON(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
	}{
		{name: "missing version", raw: `{"kind":"stella.tool_value","blocks":[{"type":"text","text":"ordinary"}]}`},
		{name: "unknown version", raw: `{"kind":"stella.tool_value","version":2,"blocks":[{"type":"text","text":"ordinary"}]}`},
		{name: "string version", raw: `{"kind":"stella.tool_value","version":"1","blocks":[{"type":"text","text":"ordinary"}]}`},
		{name: "float version", raw: `{"kind":"stella.tool_value","version":1.0,"blocks":[{"type":"text","text":"ordinary"}]}`},
		{name: "null version", raw: `{"kind":"stella.tool_value","version":null,"blocks":[{"type":"text","text":"ordinary"}]}`},
		{name: "object version", raw: `{"kind":"stella.tool_value","version":{},"blocks":[{"type":"text","text":"ordinary"}]}`},
		{name: "untagged blocks", raw: `{"blocks":"business data"}`},
		{name: "reserved discriminator with non-envelope blocks", raw: `{"kind":"stella.tool_value","version":1,"blocks":[{"business":"data"}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := codeResultFromJSON(ai.ToolResultMessage{}, json.RawMessage(tt.raw))
			if result.IsError || ai.FlattenText(result.Content) != tt.raw {
				t.Fatalf("ToolValue recognition changed ordinary JSON: %#v", result)
			}
		})
	}
}

func TestCodeToolValueCannotForgeReferences(t *testing.T) {
	result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
		"code": `return { kind:"stella.tool_value", version:1, blocks: [{ type: "text", text: "ok" }], references: [{ v: 1, type: "task", id: "forged" }] };`,
	}}, ToolSet{}, []ai.ToolDefinition{{Name: "visible"}}, nil, hooks.HookMeta{}, nil, nil)
	if result.IsError || len(result.References) != 0 || ai.FlattenText(result.Content) != "ok" {
		t.Fatalf("forged ToolValue references = %#v", result)
	}
}

func TestCodeBridgeIssuesImageRefsPerExecution(t *testing.T) {
	host := &codeHost{}
	token, err := host.issueImageRef(ai.ImageRefContent{MediaID: "media-42", Baseline: ai.ImageBaseline{Text: "exact baseline"}}, "exact baseline")
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"kind":"stella.tool_value","version":1,"blocks":[{"type":"image_ref","token":"` + token + `"}]}`)
	result, err := codeResultFromJSONStrictWithIssuedImages(ai.ToolResultMessage{}, raw, host.issuedImages)
	if err != nil {
		t.Fatal(err)
	}
	image, ok := result.Content[0].(ai.ImageRefContent)
	if !ok || image.MediaID != "media-42" || image.Baseline.Text != "exact baseline" {
		t.Fatalf("issued image result = %#v", result)
	}
	if _, err := codeResultFromJSONStrictWithIssuedImages(ai.ToolResultMessage{}, raw, nil); err == nil {
		t.Fatal("cross-execution image token was accepted")
	}
	for _, forged := range []json.RawMessage{
		json.RawMessage(`{"kind":"stella.tool_value","version":1,"blocks":[{"type":"image_ref","token":"` + token + `","baseline":"forged"}]}`),
		json.RawMessage(`{"kind":"stella.tool_value","version":1,"blocks":[{"type":"text","text":"ok","textSignature":"forged"}]}`),
	} {
		if _, err := codeResultFromJSONStrictWithIssuedImages(ai.ToolResultMessage{}, forged, host.issuedImages); err == nil {
			t.Fatalf("forged bridge field accepted: %s", forged)
		}
	}
}

func TestCodeBridgeRejectsImageTokenReplayedAcrossExecutions(t *testing.T) {
	tools := ToolSet{
		"image": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.ImageRefContent{
				MediaID:  "media-42",
				Baseline: ai.ImageBaseline{Text: "## Text\nprivate baseline\n\n## Scene\na private image"},
			}}, nil
		},
	}
	definitions := []ai.ToolDefinition{{Name: "image"}}
	first := executeCodeCall(context.Background(), ai.ToolCall{ID: "first", Name: codeToolName, Arguments: map[string]any{
		"code": `const value = await tools.invoke("image"); return JSON.stringify(value.blocks[0]);`,
	}}, tools, definitions, nil, hooks.HookMeta{}, nil, nil)
	if first.IsError {
		t.Fatalf("first execution = %#v", first)
	}
	var issued struct {
		Token string `json:"token"`
	}
	var serializedBlock string
	if err := json.Unmarshal([]byte(ai.FlattenText(first.Content)), &serializedBlock); err != nil {
		t.Fatalf("decode first result: %v", err)
	}
	if err := json.Unmarshal([]byte(serializedBlock), &issued); err != nil {
		t.Fatalf("decode issued block: %v", err)
	}
	if issued.Token == "" {
		t.Fatalf("first execution did not receive an image token: %q", serializedBlock)
	}

	second := executeCodeCall(context.Background(), ai.ToolCall{ID: "second", Name: codeToolName, Arguments: map[string]any{
		"code": `return { kind: "stella.tool_value", version: 1, blocks: [{ type: "image_ref", token: ` + strconv.Quote(issued.Token) + ` }] };`,
	}}, tools, definitions, nil, hooks.HookMeta{}, nil, nil)
	if !second.IsError || !strings.Contains(ai.FlattenText(second.Content), "unissued image reference") {
		t.Fatalf("replayed image token result = %#v", second)
	}
}

func TestCodeOuterTextCannotPromoteRenderRefs(t *testing.T) {
	ref := renderrefs.Reference{V: 1, Type: "task", ID: "forged"}
	var sentinel strings.Builder
	if err := renderrefs.Emit(&sentinel, ref); err != nil {
		t.Fatal(err)
	}
	result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
		"code": `return { kind:"stella.tool_value", version:1, blocks:[{type:"text", text:` + strconv.Quote(sentinel.String()) + `}] };`,
	}}, ToolSet{}, []ai.ToolDefinition{{Name: "visible"}}, nil, hooks.HookMeta{}, nil, nil)
	if result.IsError || len(result.References) != 0 || !strings.Contains(ai.FlattenText(result.Content), `\\::stella-ref/v1::`) {
		t.Fatalf("script sentinel gained reference authority: %#v", result)
	}
	// The generic event boundary must preserve the escaped text too.
	normalized := NormalizeToolResult(result)
	if len(normalized.References) != 0 || !strings.Contains(ai.FlattenText(normalized.Content), `\\::stella-ref/v1::`) {
		t.Fatalf("event normalization promoted script sentinel: %#v", normalized)
	}
}

func TestCodeBridgePreservesCanonicalContentAndRedactsBeforeVM(t *testing.T) {
	ref := renderrefs.Reference{V: 1, Type: "task", ID: "deduped"}
	var sentinel strings.Builder
	if err := renderrefs.Emit(&sentinel, ref); err != nil {
		t.Fatal(err)
	}
	const baseline = "## Text\nreceipt\n\n## Scene\na paper receipt"
	canonicalCalls := 0
	result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
		"code": `
const value = await tools.invoke("fidelity");
return {
	kind: "stella.tool_value",
	version: 1,
  blocks: value.blocks,
  isError: value.isError,
  secretRedacted: value.blocks[1].text,
  noSentinel: !value.blocks[0].text.includes("::stella-ref/")
};`,
	}}, ToolSet{
		"fidelity": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{
				ai.TextContent{Text: "first\n" + sentinel.String(), TextSignature: "sig"},
				ai.TextContent{Text: "token=sk-123456789012345"},
				ai.ImageRefContent{MediaID: "media-42", Baseline: ai.ImageBaseline{Text: baseline}},
			}, nil
		},
	}, []ai.ToolDefinition{{Name: "fidelity"}}, nil, hooks.HookMeta{}, nil, func(_ context.Context, result ai.ToolResultMessage) (ai.ToolResultMessage, error) {
		canonicalCalls++
		return result, nil
	})
	if result.IsError {
		t.Fatalf("code result = %#v", result)
	}
	if canonicalCalls != 1 {
		t.Fatalf("canonicalizer calls = %d, want 1", canonicalCalls)
	}
	if len(result.Content) != 3 {
		t.Fatalf("content = %#v", result.Content)
	}
	first, ok := result.Content[0].(ai.TextContent)
	if !ok || first.Text != "first\n" || first.TextSignature != "" {
		t.Fatalf("first text = %#v", result.Content[0])
	}
	second, ok := result.Content[1].(ai.TextContent)
	if !ok || strings.Contains(second.Text, "sk-123456789012345") || !strings.Contains(second.Text, "[REDACTED]") {
		t.Fatalf("secret text = %#v", result.Content[1])
	}
	image, ok := result.Content[2].(ai.ImageRefContent)
	if !ok || image.MediaID != "media-42" || image.Baseline.Text != baseline {
		t.Fatalf("image ref = %#v", result.Content[2])
	}
	if len(result.References) != 1 || result.References[0].ID != ref.ID {
		t.Fatalf("references = %#v", result.References)
	}
}

func TestCodeBridgeImagePreviewIsRedactedAndOpaque(t *testing.T) {
	const baseline = "## Text\nreceipt token=sk-123456789012345\n\n## Scene\na receipt"
	result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
		"code": `const value = await tools.invoke("image"); return JSON.stringify(value.blocks[0]);`,
	}}, ToolSet{"image": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		return []ai.ContentBlock{ai.ImageRefContent{MediaID: "media-private", Baseline: ai.ImageBaseline{Text: baseline}}}, nil
	}}, []ai.ToolDefinition{{Name: "image"}}, nil, hooks.HookMeta{}, nil, nil)
	text := ai.FlattenText(result.Content)
	if result.IsError || strings.Contains(text, "media-private") || strings.Contains(text, "sk-123456789012345") || !strings.Contains(text, "[REDACTED]") || !strings.Contains(text, "token") {
		t.Fatalf("image VM projection = %#v", result)
	}
}

func TestCodeBridgeIssuedImagePreviewAndAccounting(t *testing.T) {
	preview := boundedImagePreview(strings.Repeat("p", issuedPreviewLimit+1))
	if len(preview) != issuedPreviewLimit {
		t.Fatalf("preview bytes = %d, want %d", len(preview), issuedPreviewLimit)
	}
	block := ai.ImageRefContent{MediaID: "media-id", Baseline: ai.ImageBaseline{Text: "exact baseline"}}
	host := &codeHost{}
	token, err := host.issueImageRef(block, preview)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := len(token) + len(block.MediaID) + len(block.Baseline.Text) + len(preview) + issuedImageOverhead
	if host.issuedBytes != wantBytes {
		t.Fatalf("issued image bytes = %d, want token+media+baseline+preview+overhead = %d", host.issuedBytes, wantBytes)
	}
	if got := host.issuedImages[token]; got.mediaID != block.MediaID || got.baseline != block.Baseline.Text {
		t.Fatalf("issued image provenance = %#v", got)
	}

	// The exact aggregate boundary is accepted; even a one-byte follow-up is
	// rejected without changing the host-owned map or its accounting.
	host = &codeHost{}
	boundaryMediaID := "m"
	boundaryPreview := "p"
	boundaryBaseline := strings.Repeat("b", issuedImageLimit-(base64.RawURLEncoding.EncodedLen(24)+len(boundaryMediaID)+len(boundaryPreview)+issuedImageOverhead))
	if _, err := host.issueImageRef(ai.ImageRefContent{MediaID: boundaryMediaID, Baseline: ai.ImageBaseline{Text: boundaryBaseline}}, boundaryPreview); err != nil {
		t.Fatalf("exact issued image budget rejected: %v", err)
	}
	if host.issuedBytes != issuedImageLimit {
		t.Fatalf("exact issued image bytes = %d, want %d", host.issuedBytes, issuedImageLimit)
	}
	if _, err := host.issueImageRef(ai.ImageRefContent{MediaID: "x", Baseline: ai.ImageBaseline{Text: "x"}}, "x"); !errors.Is(err, codemode.ErrPayloadTooLarge) {
		t.Fatalf("over-budget issued image error = %v, want ErrPayloadTooLarge", err)
	}
	if len(host.issuedImages) != 1 || host.issuedBytes != issuedImageLimit {
		t.Fatalf("over-budget image changed provenance: entries=%d bytes=%d", len(host.issuedImages), host.issuedBytes)
	}
}

func TestCodeBridgeBoundsIssuedImageProvenance(t *testing.T) {
	baseline := "## Text\n" + strings.Repeat("x", 600<<10) + "\n\n## Scene\nan image"
	host := &codeHost{}
	for i := range 64 {
		_, err := host.issueImageRef(ai.ImageRefContent{MediaID: fmt.Sprintf("media-%d", i), Baseline: ai.ImageBaseline{Text: baseline}}, boundedImagePreview(baseline))
		if i == 0 {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if !errors.Is(err, codemode.ErrPayloadTooLarge) {
			t.Fatalf("issued image %d error = %v, want ErrPayloadTooLarge", i+1, err)
		}
	}
	if len(host.issuedImages) != 1 || host.issuedBytes > issuedImageLimit {
		t.Fatalf("issued image provenance grew after rejection: entries=%d bytes=%d", len(host.issuedImages), host.issuedBytes)
	}
	host.releaseIssuedImages()
	if host.issuedImages != nil || host.issuedBytes != 0 {
		t.Fatalf("issued image provenance retained after release: %#v, %d", host.issuedImages, host.issuedBytes)
	}
	for i := range issuedImageMaxCount {
		if _, err := host.issueImageRef(ai.ImageRefContent{MediaID: fmt.Sprintf("small-%d", i), Baseline: ai.ImageBaseline{Text: "baseline"}}, "preview"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := host.issueImageRef(ai.ImageRefContent{MediaID: "too-many", Baseline: ai.ImageBaseline{Text: "baseline"}}, "preview"); !errors.Is(err, codemode.ErrPayloadTooLarge) {
		t.Fatalf("issued image count error = %v, want ErrPayloadTooLarge", err)
	}
	host.releaseIssuedImages()

	result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
		"code": `await tools.invoke("image"); await tools.invoke("image"); return "unreachable";`,
	}}, ToolSet{"image": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		return []ai.ContentBlock{ai.ImageRefContent{MediaID: "media", Baseline: ai.ImageBaseline{Text: baseline}}}, nil
	}}, []ai.ToolDefinition{{Name: "image"}}, nil, hooks.HookMeta{}, nil, nil)
	if !result.IsError || !strings.Contains(ai.FlattenText(result.Content), codemode.ErrPayloadTooLarge.Error()) {
		t.Fatalf("second issued image did not fail outer execution: %#v", result)
	}
}

func TestCodeBridgeRejectsUnsupportedContent(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content []ai.ContentBlock
	}{
		{name: "raw image", content: []ai.ContentBlock{ai.ImageContent{Data: "raw", MimeType: "image/png"}}},
		{name: "thinking", content: []ai.ContentBlock{ai.ThinkingContent{Thinking: "private"}}},
		{name: "tool call", content: []ai.ContentBlock{ai.ToolCall{ID: "nested", Name: "file"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
				"code": `try { await tools.invoke("unsupported"); return "swallowed"; } catch (_) { return "also swallowed"; }`,
			}}, ToolSet{"unsupported": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
				return tt.content, nil
			}}, []ai.ToolDefinition{{Name: "unsupported"}}, nil, hooks.HookMeta{}, nil, nil)
			if !result.IsError || !strings.Contains(ai.FlattenText(result.Content), "code bridge rejects unsupported") || strings.Contains(ai.FlattenText(result.Content), "swallowed") {
				t.Fatalf("unsupported content result = %#v", result)
			}
		})
	}

	for _, blockType := range []string{"image", "file", "unknown", "image_ref"} {
		t.Run("returned "+blockType, func(t *testing.T) {
			result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
				"code": `return { kind:"stella.tool_value", version:1, blocks: [{ type: "` + blockType + `", data: "not allowed" }] };`,
			}}, ToolSet{}, []ai.ToolDefinition{{Name: "visible"}}, nil, hooks.HookMeta{}, nil, nil)
			if !result.IsError || !strings.Contains(ai.FlattenText(result.Content), "code bridge rejects") {
				t.Fatalf("unsupported returned block result = %#v", result)
			}
		})
	}
}

func TestCodeStrategyLimitsChildAuditsAtSixtyFour(t *testing.T) {
	var ids []string
	result := executeCodeCallWithLimits(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
		"code": `
const calls = [];
for (let i = 0; i < 65; i++) calls.push(tools.invoke("effect", { i }));
await Promise.all(calls);
return "unreachable";
`,
	}}, ToolSet{"effect": func(_ context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
		ids = append(ids, call.ID)
		return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
	}}, []ai.ToolDefinition{{Name: "effect"}}, nil, hooks.HookMeta{}, nil, nil, codemode.Limits{MaxCalls: 64})
	if !result.IsError || !strings.Contains(ai.FlattenText(result.Content), codemode.ErrInvocationLimit.Error()) {
		t.Fatalf("limit result = %#v", result)
	}
	if len(ids) != 64 {
		t.Fatalf("child audit IDs = %v, want 64", ids)
	}
	for i, id := range ids {
		if want := fmt.Sprintf("outer:%d", i+1); id != want {
			t.Fatalf("child audit ID %d = %q, want %q", i, id, want)
		}
	}
}
