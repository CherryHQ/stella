package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/codemode"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

// Code Mode is the only strategy, so the provider never sees the cold tools
// directly: they reach the model through the code tool's catalog.
func TestProviderToolVisibilityIsCodeOnly(t *testing.T) {
	definitions := []ai.ToolDefinition{{Name: "one"}, {Name: "two"}}
	toolSet := ToolSet{
		"one": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) { return nil, nil },
		"two": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) { return nil, nil },
	}
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
	runner, err := NewRunner(RunnerConfig{Stream: stream, Tools: toolSet, ToolDefinitions: definitions})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil); err != nil {
		t.Fatal(err)
	}
	if len(seen.Tools) != 1 || seen.Tools[0].Name != codeToolName {
		t.Fatalf("provider tools = %#v, want only %q", seen.Tools, codeToolName)
	}
}

func TestCodeToolDescriptionMatchesHotRoutingPolicy(t *testing.T) {
	for _, guidance := range []string{
		"Native tools handle standalone work",
		"never wrap a standalone native call in Code",
		"Search once when the capability or name is unknown",
		"If the exact name is known but its input schema is not, describe it directly",
		"30 seconds wall clock including child tools",
	} {
		if !strings.Contains(codeToolDefinition.Description, guidance) {
			t.Fatalf("code description lost routing guidance %q", guidance)
		}
	}
}

// The description is the model's only prose about which tools stay native, and
// prose does not compile: it kept naming `skills` after that tool was split
// away. Parse the sentence and compare it name by name, so a generic phrase
// check cannot pass while the list underneath it has drifted.
func TestCodeToolDescriptionNamesExactlyTheHotTools(t *testing.T) {
	const (
		prefix = "Hot keeps "
		suffix = " native."
	)
	start := strings.Index(codeToolDefinition.Description, prefix)
	if start < 0 {
		t.Fatalf("code description no longer says %q, so the hot set is undocumented", prefix)
	}
	rest := codeToolDefinition.Description[start+len(prefix):]
	before, _, ok := strings.Cut(rest, suffix)
	if !ok {
		t.Fatalf("code description opens the hot-set sentence but never closes it with %q", suffix)
	}
	var got []string
	for field := range strings.SplitSeq(before, ",") {
		name := strings.TrimPrefix(strings.TrimSpace(field), "and ")
		if name = strings.TrimSpace(name); name != "" {
			got = append(got, name)
		}
	}
	slices.Sort(got)
	want := slices.Clone(HotToolNames)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("code description claims hot tools %v, want exactly %v", got, want)
	}
}

func TestCodeChildArgumentRedactionHandlesJSONEscapedSecrets(t *testing.T) {
	secret := "abc\"def\\ghi\nend"
	got := redactChildArguments(map[string]any{"command": "printf " + secret, "nested": []any{secret}}, []string{secret})
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "abc") || strings.Contains(string(raw), "def") {
		t.Fatalf("escaped secret leaked: %s", raw)
	}
}

func TestCodeModeExposesBashDirectlyAndInsideCode(t *testing.T) {
	calls := 0
	bashCalls := 0
	specialCalls := 0
	stream := func(_ context.Context, _ ai.Model, request ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		if len(request.Tools) != 2 || request.Tools[0].Name != "bash" || request.Tools[1].Name != codeToolName {
			t.Fatalf("provider tools = %#v, want bash and code", request.Tools)
		}
		calls++
		out := providers.NewChannelEventStream(4)
		go func() {
			switch calls {
			case 1:
				out.Emit(ai.EventToolCallDelta{ID: "direct", Name: "bash", Arguments: `{"command":"pwd"}`})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			case 2:
				source := `const names = tools.search("").map(t => t.name); const described = tools.describe("bash"); const shell = await tools.invoke("bash", {command:"pwd"}); const value = await tools.invoke("special", {}); return {names, described:described.name, shell, value};`
				raw, _ := json.Marshal(map[string]string{"code": source})
				out.Emit(ai.EventToolCallDelta{ID: "outer", Name: codeToolName, Arguments: string(raw)})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			default:
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
			"bash": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
				bashCalls++
				return []ai.ContentBlock{ai.TextContent{Text: "direct"}}, nil
			},
			"special": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
				specialCalls++
				return []ai.ContentBlock{ai.TextContent{Text: "specialized"}}, nil
			},
		},
		ToolDefinitions: []ai.ToolDefinition{{Name: "bash"}, {Name: "special", Description: "specialized"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	history, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil)
	if err != nil || calls != 3 || bashCalls != 2 || specialCalls != 1 {
		t.Fatalf("journey err=%v provider=%d bash=%d special=%d history=%#v", err, calls, bashCalls, specialCalls, history)
	}
	var codeResult ai.ToolResultMessage
	for _, message := range history {
		if result, ok := message.(ai.ToolResultMessage); ok && result.ToolName == codeToolName {
			codeResult = result
		}
	}
	text := ai.FlattenText(codeResult.Content)
	if !strings.Contains(text, `"names":["bash","special"]`) || !strings.Contains(text, `"described":"bash"`) {
		t.Fatalf("code result = %q, bash missing from complete catalog", text)
	}
}

func TestCodeChildEventsAreRedactedAndNotAddedToHistory(t *testing.T) {
	calls := 0
	stream := func(_ context.Context, _ ai.Model, request ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		calls++
		out := providers.NewChannelEventStream(3)
		go func() {
			if calls == 1 {
				source := `await tools.invoke("bash", {command:"printf top-secret; printf ' token=opaque-value'"}); return "ok";`
				raw, _ := json.Marshal(map[string]string{"code": source})
				out.Emit(ai.EventToolCallDelta{ID: "outer", Name: codeToolName, Arguments: string(raw)})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		if calls == 1 && (len(request.Tools) != 2 || request.Tools[0].Name != "bash" || request.Tools[1].Name != codeToolName) {
			t.Fatalf("provider tools = %#v", request.Tools)
		}
		return out, nil
	}
	runner, err := NewRunner(RunnerConfig{
		Stream: stream,
		Tools: ToolSet{
			"bash": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
				return []ai.ContentBlock{ai.TextContent{Text: "top-secret token=opaque-value"}}, nil
			},
			"special": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) { return nil, nil },
		},
		ToolDefinitions: []ai.ToolDefinition{{Name: "bash"}, {Name: "special"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.SetSecretValues([]string{"top-secret"})
	var events []LoopEvent
	history, err := runner.RunWithActiveStart(context.Background(), []ai.Message{ai.UserMessage{Content: "go"}}, 0, func(event LoopEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	childStarts := 0
	childFinishes := 0
	for _, event := range events {
		switch event := event.(type) {
		case ChildToolStarted:
			childStarts++
			raw, _ := json.Marshal(event.ToolCall.Arguments)
			if strings.Contains(string(raw), "top-secret") || strings.Contains(string(raw), "opaque-value") {
				t.Fatalf("child arguments leaked: %s", raw)
			}
		case ChildToolFinished:
			childFinishes++
			if text := ai.FlattenText(event.Result.Content); strings.Contains(text, "top-secret") || strings.Contains(text, "opaque-value") {
				t.Fatalf("child result leaked: %q", text)
			}
		}
	}
	if childStarts != 1 || childFinishes != 1 {
		t.Fatalf("child events = starts:%d finishes:%d", childStarts, childFinishes)
	}
	for _, message := range history {
		if result, ok := message.(ai.ToolResultMessage); ok && result.ToolName == "bash" {
			t.Fatalf("child result entered provider history: %#v", history)
		}
	}
}

func TestCodeModeMixedCallsPreserveDirectThenSpecializedOrder(t *testing.T) {
	var order []string
	direct := ToolSet{"bash": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		order = append(order, "bash")
		return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
	}}
	specialized := ToolSet{"special": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		order = append(order, "special")
		return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
	}}
	results, err := executeCodeModeCalls(context.Background(), []ai.ToolCall{
		{ID: "direct", Name: "bash", Arguments: map[string]any{}},
		{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `return await tools.invoke("special", {});`}},
	}, direct, specialized, []ai.ToolDefinition{{Name: "special"}}, toolCallbacks{}, nil, hooks.HookMeta{}, nil, nil)
	if err != nil || len(results) != 2 || strings.Join(order, ",") != "bash,special" {
		t.Fatalf("results=%#v err=%v order=%v", results, err, order)
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
			})
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

func TestCodeResultCarriesBoundedChildAuditWithoutArguments(t *testing.T) {
	result := executeCodeCall(context.Background(), ai.ToolCall{
		ID: "outer", Name: codeToolName, Arguments: map[string]any{
			"code": `return await tools.invoke("echo", { secret: "must-not-persist" });`,
		},
	}, ToolSet{"echo": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
	}}, []ai.ToolDefinition{{Name: "echo"}}, nil, hooks.HookMeta{}, nil, nil)
	if result.IsError {
		t.Fatalf("code result = %#v", result)
	}
	if len(result.ChildToolCalls) != 1 {
		t.Fatalf("child audit = %#v, want one entry", result.ChildToolCalls)
	}
	got := result.ChildToolCalls[0]
	if got.ID != "outer:1" || got.Name != "echo" || got.IsError || got.ErrorKind != "" {
		t.Fatalf("child audit = %#v", got)
	}
	encoded, err := json.Marshal(result.ChildToolCalls)
	if err != nil || strings.Contains(string(encoded), "must-not-persist") {
		t.Fatalf("child audit leaked arguments: %s (%v)", encoded, err)
	}
}

func TestCodeChildAuditRecordsEveryAcceptedInvocationOutcome(t *testing.T) {
	for _, tt := range []struct {
		name  string
		code  string
		tools ToolSet
		defs  []ai.ToolDefinition
	}{
		{name: "argument decode", code: `await tools.invoke("effect", []);`, tools: ToolSet{"effect": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			t.Fatal("invalid arguments reached tool")
			return nil, nil
		}}, defs: []ai.ToolDefinition{{Name: "effect"}}},
		{name: "tool missing", code: `await tools.invoke("missing", {});`, tools: ToolSet{}, defs: []ai.ToolDefinition{{Name: "missing"}}},
		{name: "business error", code: `await tools.invoke("effect", {});`, tools: ToolSet{"effect": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return nil, errors.New("business failure")
		}}, defs: []ai.ToolDefinition{{Name: "effect"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": tt.code}}, tt.tools, tt.defs, nil, hooks.HookMeta{}, nil, nil)
			if len(result.ChildToolCalls) != 1 {
				t.Fatalf("audit = %#v, want one accepted attempt", result.ChildToolCalls)
			}
			audit := result.ChildToolCalls[0]
			if audit.ID != "outer:1" || !audit.IsError || audit.ErrorKind != ai.ToolErrorKindTool {
				t.Fatalf("audit = %#v, want classified failed attempt", audit)
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
	}, WithHooks(hooks.NewHookSet([]hooks.HookPlugin{hook}), hooks.HookMeta{}))
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
	}, WithHooks(hooks.NewHookSet([]hooks.HookPlugin{hook}), hooks.HookMeta{}))
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
	}, WithHooks(hooks.NewHookSet([]hooks.HookPlugin{hook}), hooks.HookMeta{}))
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

func TestCodeBlockedChildDoesNotClaimSideEffectsCommitted(t *testing.T) {
	result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
		"code": `await tools.invoke("blocked", {});`,
	}}, ToolSet{"blocked": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		t.Fatal("blocked handler executed")
		return nil, nil
	}}, []ai.ToolDefinition{{Name: "blocked"}}, nil, hooks.HookMeta{}, &ToolLifecycle{BeforeCall: func(context.Context, ToolCallContext) (ToolCallMutation, error) {
		return ToolCallMutation{Block: true, BlockMessage: "blocked by policy"}, nil
	}}, nil)
	if !result.IsError || strings.Contains(ai.FlattenText(result.Content), childEffectNotice) {
		t.Fatalf("blocked result claimed side effects: %#v", result)
	}
	details, ok := result.Details.(codeExecutionDetails)
	if !ok || details.ChildSideEffectsMayHaveCommitted {
		t.Fatalf("blocked details = %#v", result.Details)
	}
}

func TestCodeSettledChildAttemptsEmitPairedEvents(t *testing.T) {
	for _, tt := range []struct {
		name      string
		tools     ToolSet
		lifecycle *ToolLifecycle
	}{
		{name: "missing", tools: ToolSet{}},
		{name: "blocked", tools: ToolSet{"child": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			t.Fatal("blocked handler executed")
			return nil, nil
		}}, lifecycle: &ToolLifecycle{BeforeCall: func(context.Context, ToolCallContext) (ToolCallMutation, error) {
			return ToolCallMutation{Block: true, BlockMessage: "blocked"}, nil
		}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var events []string
			result := executeCodeCallWithCallbacks(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
				"code": `await tools.invoke("child", {});`,
			}}, tt.tools, []ai.ToolDefinition{{Name: "child"}}, toolCallbacks{
				onChildStart:  func(string, ai.ToolCall) { events = append(events, "start") },
				onChildFinish: func(string, ai.ToolResultMessage) { events = append(events, "finish") },
			}, nil, hooks.HookMeta{}, tt.lifecycle, nil)
			if !result.IsError || strings.Join(events, ",") != "start,finish" {
				t.Fatalf("result=%#v events=%v", result, events)
			}
			details, ok := result.Details.(codeExecutionDetails)
			if !ok || details.ChildSideEffectsMayHaveCommitted {
				t.Fatalf("settled attempt details=%#v", result.Details)
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
		if len(result.ChildToolCalls) != 1 || result.ChildToolCalls[0].ID != "outer:1" {
			t.Fatalf("accepted child attempt missing from audit: %#v", result.ChildToolCalls)
		}
	}

	t.Run("throw", func(t *testing.T) {
		assertFailedEffect(t, executeCodeCall(context.Background(), call(`await tools.invoke("effect"); throw new Error("after effect");`), tools, []ai.ToolDefinition{{Name: "effect"}}, nil, hooks.HookMeta{}, nil, nil))
	})
	t.Run("limit", func(t *testing.T) {
		ctx := newTriggeredDeadlineContext()
		started := make(chan struct{})
		release := make(chan struct{})
		limitTools := ToolSet{"effect": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			close(started)
			<-release
			return []ai.ContentBlock{ai.TextContent{Text: "created\n" + sentinel.String()}}, nil
		}}
		resultCh := make(chan ai.ToolResultMessage, 1)
		go func() {
			resultCh <- executeCodeCallWithLimits(ctx, call(`await tools.invoke("effect"); while (true) {}`), limitTools, []ai.ToolDefinition{{Name: "effect"}}, nil, hooks.HookMeta{}, nil, nil, codemode.Limits{WallClock: time.Second})
		}()
		select {
		case <-started:
			ctx.expire()
			close(release)
		case <-time.After(time.Second):
			t.Fatal("child did not reach the host-start barrier")
		}
		select {
		case result := <-resultCh:
			assertFailedEffect(t, result)
		case <-time.After(time.Second):
			t.Fatal("deadline-triggered code call did not return")
		}
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

// triggeredDeadlineContext lets this test trigger deadline semantics only after
// the child has crossed its host-start barrier. It avoids scheduler-dependent
// short wall-clock races while exercising the production timeout path.
type triggeredDeadlineContext struct {
	context.Context
	done    chan struct{}
	once    sync.Once
	expired atomic.Bool
}

func newTriggeredDeadlineContext() *triggeredDeadlineContext {
	return &triggeredDeadlineContext{Context: context.Background(), done: make(chan struct{})}
}

func (c *triggeredDeadlineContext) Done() <-chan struct{} { return c.done }
func (c *triggeredDeadlineContext) Err() error {
	if c.expired.Load() {
		return context.DeadlineExceeded
	}
	return nil
}

func (c *triggeredDeadlineContext) expire() {
	c.once.Do(func() {
		c.expired.Store(true)
		close(c.done)
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
			if !result.IsError || !strings.Contains(ai.FlattenText(result.Content), "code tool infrastructure failure") || strings.Contains(ai.FlattenText(result.Content), "swallowed") {
				t.Fatalf("infrastructure failure was catchable: %#v", result)
			}
			if len(result.ChildToolCalls) != 1 || result.ChildToolCalls[0].ID != "outer:1" {
				t.Fatalf("infrastructure failure lost accepted child attempt: %#v", result.ChildToolCalls)
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
		runner, err := NewRunner(RunnerConfig{Stream: stream, Tools: toolSet, ToolDefinitions: []ai.ToolDefinition{{Name: "visible"}, {Name: "hidden"}}}, WithHooks(hooks.NewHookSet([]hooks.HookPlugin{hook}), hooks.HookMeta{}))
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

func TestCodeBridgePaginationTokenRoundTrips(t *testing.T) {
	calls := 0
	result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
		"code": `
const first = tools.json(await tools.invoke("page", {}));
const second = tools.json(await tools.invoke("page", { page_token: first.next_page_token }));
return second;
`,
	}}, ToolSet{"page": func(_ context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
		calls++
		if calls == 1 {
			return []ai.ContentBlock{ai.TextContent{Text: `{"items":[1],"next_page_token":"eyJvIjoyfQ"}`}}, nil
		}
		if call.Arguments["page_token"] != "eyJvIjoyfQ" {
			t.Fatalf("page token = %#v, want unchanged", call.Arguments["page_token"])
		}
		return []ai.ContentBlock{ai.TextContent{Text: `{"items":[2]}`}}, nil
	}}, []ai.ToolDefinition{{Name: "page"}}, nil, hooks.HookMeta{}, nil, nil)
	if result.IsError || calls != 2 || ai.FlattenText(result.Content) != `{"items":[2]}` {
		t.Fatalf("pagination result = %#v, calls = %d", result, calls)
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
	if !second.IsError || !strings.Contains(ai.FlattenText(second.Content), "code tool infrastructure failure") {
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
	if !result.IsError || !strings.Contains(ai.FlattenText(result.Content), "fixed payload byte limit") {
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
			if !result.IsError || !strings.Contains(ai.FlattenText(result.Content), "code tool infrastructure failure") || strings.Contains(ai.FlattenText(result.Content), "swallowed") {
				t.Fatalf("unsupported content result = %#v", result)
			}
		})
	}

	for _, blockType := range []string{"image", "file", "unknown", "image_ref"} {
		t.Run("returned "+blockType, func(t *testing.T) {
			result := executeCodeCall(context.Background(), ai.ToolCall{ID: "outer", Name: codeToolName, Arguments: map[string]any{
				"code": `return { kind:"stella.tool_value", version:1, blocks: [{ type: "` + blockType + `", data: "not allowed" }] };`,
			}}, ToolSet{}, []ai.ToolDefinition{{Name: "visible"}}, nil, hooks.HookMeta{}, nil, nil)
			if !result.IsError || !strings.Contains(ai.FlattenText(result.Content), "code tool infrastructure failure") {
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
	if !result.IsError || !strings.Contains(ai.FlattenText(result.Content), "fixed child-call limit") {
		t.Fatalf("limit result = %#v", result)
	}
	if len(ids) > 1 {
		t.Fatalf("owner fatal started queued child calls = %v, want at most the inflight call", ids)
	}
	if len(result.ChildToolCalls) > 64 {
		t.Fatalf("child audit escaped executor call ceiling: %d entries", len(result.ChildToolCalls))
	}
}

func TestCodeExecutionLimitDiagnosticsAreDistinct(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		code string
	}{
		{name: "source", err: &codemode.LimitError{Err: codemode.ErrSourceTooLarge, Code: "code_source_too_large", Actual: 11, Limit: 10}, code: "code_source_too_large"},
		{name: "payload", err: &codemode.LimitError{Err: codemode.ErrPayloadTooLarge, Code: "code_payload_too_large", Actual: 11, Limit: 10}, code: "code_payload_too_large"},
		{name: "result", err: &codemode.LimitError{Err: codemode.ErrResultTooLarge, Code: "code_result_too_large", Actual: 11, Limit: 10}, code: "code_result_too_large"},
		{name: "calls", err: &codemode.LimitError{Err: codemode.ErrInvocationLimit, Code: "code_invocation_limit", Actual: 65, Limit: 64}, code: "code_invocation_limit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := codeExecutionError(ai.ToolResultMessage{}, &codeHost{}, tt.err)
			details, ok := result.Details.(codeExecutionDetails)
			if !ok || details.Code != tt.code || details.Actual == 0 || details.Limit == 0 {
				t.Fatalf("limit details = %#v", result.Details)
			}
		})
	}
}

func TestCodeModeHotToolsAreDirectAndInCompleteCatalog(t *testing.T) {
	definitions := []ai.ToolDefinition{{Name: "bash"}, {Name: "skill_load"}, {Name: "memory_search"}, {Name: "memory_read"}, {Name: "view_image"}, {Name: "recally_feed_list"}}
	tools := make(ToolSet, len(definitions))
	for _, definition := range definitions {
		tools[definition.Name] = func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) { return nil, nil }
	}
	direct, codeTools, providerDefs, codeDefs := codeModeToolSurface(tools, definitions, CodeToolSurfaceHot)
	for _, name := range HotToolNames {
		if direct[name] == nil || codeTools[name] == nil {
			t.Fatalf("hot tool %q missing from direct/code surfaces", name)
		}
	}
	if direct["recally_feed_list"] != nil || codeTools["recally_feed_list"] == nil {
		t.Fatalf("cold tool surfaces direct=%v code=%v", direct["recally_feed_list"] != nil, codeTools["recally_feed_list"] != nil)
	}
	var providerNames []string
	for _, definition := range providerDefs {
		providerNames = append(providerNames, definition.Name)
	}
	if got, want := strings.Join(providerNames, ","), "bash,skill_load,memory_search,memory_read,view_image,code"; got != want {
		t.Fatalf("provider tools = %q, want %q", got, want)
	}
	if len(codeDefs) != len(definitions) {
		t.Fatalf("code catalog definitions = %d, want %d", len(codeDefs), len(definitions))
	}
}

// TestCodeModeHidesSyntheticToolForBashOnlyCatalog pins that the code tool is
// offered only when it hides something. bash stays native, so a bash-only tool
// set leaves the catalog empty and the tool must not be advertised.
func TestCodeModeHidesSyntheticToolForBashOnlyCatalog(t *testing.T) {
	tools := ToolSet{"bash": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) { return nil, nil }}
	definitions := []ai.ToolDefinition{{Name: "bash"}}
	_, _, providerDefs, codeDefs := codeModeToolSurface(tools, definitions, CodeToolSurfaceHot)
	if len(codeDefs) != 0 {
		t.Fatalf("code catalog = %#v, want empty", codeDefs)
	}
	if len(providerDefs) != 1 || providerDefs[0].Name != "bash" {
		t.Fatalf("provider tools = %#v, want bash only", providerDefs)
	}
}

func TestCodeModeEvaluationSurfacesKeepCompleteCatalog(t *testing.T) {
	definitions := []ai.ToolDefinition{{Name: "bash"}, {Name: "skill_load"}, {Name: "recally_feed_list"}}
	tools := make(ToolSet, len(definitions))
	for _, definition := range definitions {
		tools[definition.Name] = func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) { return nil, nil }
	}
	for _, tt := range []struct {
		name    string
		surface CodeToolSurface
		want    string
	}{
		{name: "bash and code", surface: CodeToolSurfaceBash, want: "bash,code"},
		{name: "code only", surface: CodeToolSurfaceOnly, want: "code"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			direct, codeTools, providerDefs, codeDefs := codeModeToolSurface(tools, definitions, tt.surface)
			var providerNames []string
			for _, definition := range providerDefs {
				providerNames = append(providerNames, definition.Name)
			}
			if got := strings.Join(providerNames, ","); got != tt.want {
				t.Fatalf("provider tools = %q, want %q", got, tt.want)
			}
			if len(codeTools) != len(tools) || len(codeDefs) != len(definitions) {
				t.Fatalf("complete catalog tools=%d defs=%d", len(codeTools), len(codeDefs))
			}
			if tt.surface == CodeToolSurfaceOnly && len(direct) != 0 {
				t.Fatalf("direct tools = %#v, want none", direct)
			}
		})
	}
}

// TestCodeExecutionOutcomeTerminality separates the two exits that used to be
// treated alike. A timeout is a tool error the model can respond to; only an
// interrupted turn is terminal, because there the caller is already gone.
func TestCodeExecutionOutcomeTerminality(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      error
		wantCode string
		terminal bool
	}{
		{name: "timeout", err: codemode.ErrTimedOut, wantCode: "code_execution_timed_out"},
		{name: "cancel", err: codemode.ErrCancelled, wantCode: "code_execution_cancelled", terminal: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := codeExecutionError(ai.ToolResultMessage{ToolCallID: "outer", ToolName: codeToolName}, &codeHost{}, tt.err)
			details, ok := result.Details.(codeExecutionDetails)
			if !ok {
				t.Fatalf("details = %#v, want codeExecutionDetails", result.Details)
			}
			if details.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", details.Code, tt.wantCode)
			}
			if details.Terminal != tt.terminal {
				t.Fatalf("terminal = %v, want %v", details.Terminal, tt.terminal)
			}
		})
	}
}

// TestCodeModeTerminalResultStopsRemainingCalls covers a provider turn that
// asked for a code call and a bash call together. Once the code call ends the
// turn, the sibling call must never start: its side effects would land after
// the caller has gone away.
func TestCodeModeTerminalResultStopsRemainingCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{})
	var bashRan atomic.Bool
	codeTools := ToolSet{"effect": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		close(called)
		return []ai.ContentBlock{ai.TextContent{Text: "created"}}, nil
	}}
	directTools := ToolSet{"bash": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		bashRan.Store(true)
		return []ai.ContentBlock{ai.TextContent{Text: "ran"}}, nil
	}}
	calls := []ai.ToolCall{
		{ID: "outer", Name: codeToolName, Arguments: map[string]any{"code": `await tools.invoke("effect"); while (true) {}`}},
		{ID: "sibling", Name: "bash", Arguments: map[string]any{"command": "echo hi"}},
	}
	type outcome struct {
		results []ai.ToolResultMessage
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		results, err := executeCodeModeCalls(ctx, calls, directTools, codeTools, []ai.ToolDefinition{{Name: "effect"}}, toolCallbacks{}, nil, hooks.HookMeta{}, nil, nil)
		done <- outcome{results: results, err: err}
	}()
	select {
	case <-called:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("child was not invoked")
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("executeCodeModeCalls error = %v, want nil", got.err)
		}
		if len(got.results) != 1 {
			t.Fatalf("results = %d, want only the terminal code result", len(got.results))
		}
		details, ok := got.results[0].Details.(codeExecutionDetails)
		if !ok || !details.Terminal {
			t.Fatalf("first result details = %#v, want a terminal code result", got.results[0].Details)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled batch did not return")
	}
	if bashRan.Load() {
		t.Fatal("sibling bash call ran after a terminal code result")
	}
}

// TestLoopStopsTurnAfterTerminalCodeResult is the loop-level half of the same
// contract: after an interrupted code call the loop must not ask the provider
// for another turn, and it must hand back the durable history rather than an
// error.
func TestLoopStopsTurnAfterTerminalCodeResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var providerCalls atomic.Int32
	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		out := providers.NewChannelEventStream(2)
		turn := providerCalls.Add(1)
		go func() {
			if turn == 1 {
				out.Emit(ai.EventToolCallDelta{ID: "outer", Name: codeToolName, Arguments: `{"code":"await tools.invoke(\"effect\"); while (true) {}"}`})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		return out, nil
	}
	tools := ToolSet{"effect": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		cancel()
		return []ai.ContentBlock{ai.TextContent{Text: "created"}}, nil
	}}
	runner, err := NewRunner(RunnerConfig{Stream: stream, Tools: tools, ToolDefinitions: []ai.ToolDefinition{{Name: "effect"}}})
	if err != nil {
		t.Fatal(err)
	}
	history, err := runner.RunWithActiveStart(ctx, []ai.Message{ai.UserMessage{Content: "go"}}, 0, nil)
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("provider turns = %d, want 1 after a terminal code result", got)
	}
	var terminal bool
	for _, message := range history {
		result, ok := message.(ai.ToolResultMessage)
		if !ok {
			continue
		}
		if details, ok := result.Details.(codeExecutionDetails); ok && details.Terminal {
			terminal = true
		}
	}
	if !terminal {
		t.Fatal("history is missing the durable terminal code result")
	}
}
