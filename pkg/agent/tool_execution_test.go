package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

func TestToolExecution(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "echo"}, {ID: "2", Name: "missing"}}
	tools := ToolSet{
		"echo": func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
		},
	}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, nil, hooks.HookMeta{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].IsError {
		t.Fatalf("expected first result success")
	}
	if !results[1].IsError {
		t.Fatalf("expected second result error for missing tool")
	}
}

func TestToolExecutionToolError(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "fail"}}
	tools := ToolSet{
		"fail": func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
			return nil, errors.New("boom")
		},
	}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, nil, hooks.HookMeta{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("expected error result")
	}
}

func TestToolExecutionPreservesContentOnError(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "bash"}}
	tools := ToolSet{
		"bash": func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.TextContent{Text: "pip: command not found"}}, errors.New("bash: exit code 127")
		},
	}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, nil, hooks.HookMeta{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatal("expected error result")
	}
	text := results[0].Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "pip: command not found") {
		t.Errorf("error result should preserve tool output, got: %q", text)
	}
	if !strings.Contains(text, "exit code 127") {
		t.Errorf("error result should contain error message, got: %q", text)
	}
}

func TestToolExecutionEmptyContentOnError(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "fail"}}
	tools := ToolSet{
		"fail": func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
			return nil, errors.New("boom")
		},
	}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, nil, hooks.HookMeta{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := results[0].Content[0].(ai.TextContent).Text
	if text != "boom" {
		t.Errorf("with empty content, should just show error, got: %q", text)
	}
}

func TestToolExecutionAppliesLifecycleMutations(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "echo", Arguments: map[string]any{"q": "original"}}}
	tools := ToolSet{
		"echo": func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
			if got := call.Arguments["q"]; got != "rewritten" {
				t.Fatalf("unexpected arguments: %#v", call.Arguments)
			}
			return []ai.ContentBlock{ai.TextContent{Text: "raw"}}, nil
		},
	}
	lifecycle := &ToolLifecycle{
		BeforeCall: func(context.Context, ToolCallContext) (ToolCallMutation, error) {
			return ToolCallMutation{Arguments: map[string]any{"q": "rewritten"}}, nil
		},
		AfterCall: func(context.Context, ToolResultContext) (ToolResultMutation, error) {
			text := "final"
			isError := false
			return ToolResultMutation{Result: &text, IsError: &isError}, nil
		},
	}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, nil, hooks.HookMeta{
		SessionID: "session-1",
		Channel:   "cli",
	}, lifecycle, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if got := results[0].Content[0].(ai.TextContent).Text; got != "final" {
		t.Fatalf("unexpected result text: %q", got)
	}
	if results[0].IsError {
		t.Fatal("expected lifecycle to clear error flag")
	}
}

func TestToolExecutionLifecycleTextMutationPreservesImages(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "read"}}
	tools := ToolSet{
		"read": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{
				ai.TextContent{Text: "Read image file [image/jpeg]"},
				ai.ImageContent{Data: "base64", MimeType: "image/jpeg"},
			}, nil
		},
	}
	lifecycle := &ToolLifecycle{AfterCall: func(context.Context, ToolResultContext) (ToolResultMutation, error) {
		text := "rewritten"
		return ToolResultMutation{Result: &text}, nil
	}}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, nil, hooks.HookMeta{}, lifecycle, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := ai.FlattenText(results[0].Content); got != "rewritten" {
		t.Fatalf("text = %q, want rewritten", got)
	}
	if !ai.HasImage(results[0].Content) {
		t.Fatalf("image block was dropped: %#v", results[0].Content)
	}
}

func TestToolExecutionTransformsFinalResultBeforeCallback(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "image"}}
	lifecycle := &ToolLifecycle{AfterCall: func(context.Context, ToolResultContext) (ToolResultMutation, error) {
		text := "after lifecycle"
		return ToolResultMutation{Result: &text}, nil
	}}
	var finished ai.ToolResultMessage
	results, err := executeToolCalls(context.Background(), calls, ToolSet{
		"image": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.ImageContent{Data: "raw", MimeType: "image/png"}}, nil
		},
	}, toolCallbacks{onFinish: func(result ai.ToolResultMessage) { finished = result }}, nil, hooks.HookMeta{}, lifecycle,
		func(_ context.Context, result ai.ToolResultMessage) (ai.ToolResultMessage, error) {
			if got := ai.FlattenText(result.Content); got != "after lifecycle" {
				t.Fatalf("transform ran before lifecycle: %q", got)
			}
			result.Content = []ai.ContentBlock{canonicalRef("media-1")}
			return result, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !containsRef(results[0].Content) || !containsRef(finished.Content) {
		t.Fatalf("final transform missing: results=%#v callback=%#v", results, finished)
	}
}

func containsRef(blocks []ai.ContentBlock) bool {
	for _, block := range blocks {
		if _, ok := block.(ai.ImageRefContent); ok {
			return true
		}
	}
	return false
}

func TestToolExecutionOrdersLifecycleBeforeAndAfterHooks(t *testing.T) {
	var order []string
	calls := []ai.ToolCall{{ID: "1", Name: "echo", Arguments: map[string]any{"q": "original"}}}
	tools := ToolSet{
		"echo": func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
			order = append(order, "tool")
			if got := call.Arguments["q"]; got != "hooked" {
				t.Fatalf("unexpected tool arguments: %#v", call.Arguments)
			}
			return []ai.ContentBlock{ai.TextContent{Text: "raw"}}, nil
		},
	}
	lifecycle := &ToolLifecycle{
		BeforeCall: func(context.Context, ToolCallContext) (ToolCallMutation, error) {
			order = append(order, "before-lifecycle")
			return ToolCallMutation{Arguments: map[string]any{"q": "lifecycle"}}, nil
		},
		AfterCall: func(context.Context, ToolResultContext) (ToolResultMutation, error) {
			order = append(order, "after-lifecycle")
			text := "final"
			return ToolResultMutation{Result: &text}, nil
		},
	}
	hs := hooks.NewHookSet([]hooks.HookPlugin{toolExecutionHook{
		pre: func(_ context.Context, hctx *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
			order = append(order, "before-hook")
			if got := hctx.Arguments["q"]; got != "lifecycle" {
				t.Fatalf("unexpected pre-hook arguments: %#v", hctx.Arguments)
			}
			return hooks.PreToolCallResult{Arguments: map[string]any{"q": "hooked"}}, nil
		},
		post: func(_ context.Context, hctx *hooks.PostToolCallContext) {
			order = append(order, "after-hook")
			if hctx.Result != "final" {
				t.Fatalf("unexpected post-hook result: %q", hctx.Result)
			}
		},
	}})

	_, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, hs, hooks.HookMeta{}, lifecycle, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.Join(order, ",")
	want := "before-lifecycle,before-hook,tool,after-lifecycle,after-hook"
	if got != want {
		t.Fatalf("order = %q, want %q", got, want)
	}
}

func TestToolExecutionRunsPostHookWhenPreHookBlocks(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "bash", Arguments: map[string]any{"command": "rm -rf /"}}}
	postCalled := false
	hs := hooks.NewHookSet([]hooks.HookPlugin{toolExecutionHook{
		pre: func(_ context.Context, _ *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
			return hooks.PreToolCallResult{Block: true, BlockMessage: "nope"}, nil
		},
		post: func(_ context.Context, hctx *hooks.PostToolCallContext) {
			postCalled = true
			if !hctx.IsError {
				t.Fatal("blocked tool should be reported as an error to post hooks")
			}
			if hctx.Result != "nope" {
				t.Fatalf("post hook result = %q, want nope", hctx.Result)
			}
		},
	}})

	results, err := executeToolCalls(context.Background(), calls, ToolSet{"bash": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		t.Fatal("blocked tool should not execute")
		return nil, nil
	}}, toolCallbacks{}, hs, hooks.HookMeta{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !postCalled {
		t.Fatal("post hook was not called")
	}
	if got := results[0].Content[0].(ai.TextContent).Text; got != "nope" {
		t.Fatalf("result = %q, want nope", got)
	}
}

func TestToolExecutionRunsPostHookWhenLifecycleAfterFails(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "echo"}}
	postCalled := false
	hs := hooks.NewHookSet([]hooks.HookPlugin{toolExecutionHook{
		pre: func(context.Context, *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
			return hooks.PreToolCallResult{}, nil
		},
		post: func(_ context.Context, hctx *hooks.PostToolCallContext) {
			postCalled = true
			if !hctx.IsError {
				t.Fatal("lifecycle failure should be reported as an error to post hooks")
			}
			if hctx.Result != "after failed" {
				t.Fatalf("post hook result = %q, want after failed", hctx.Result)
			}
		},
	}})
	lifecycle := &ToolLifecycle{AfterCall: func(context.Context, ToolResultContext) (ToolResultMutation, error) {
		return ToolResultMutation{}, errors.New("after failed")
	}}

	_, err := executeToolCalls(context.Background(), calls, ToolSet{"echo": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		return []ai.ContentBlock{ai.TextContent{Text: "raw"}}, nil
	}}, toolCallbacks{}, hs, hooks.HookMeta{}, lifecycle, nil)
	if err == nil {
		t.Fatal("expected lifecycle error")
	}
	if !postCalled {
		t.Fatal("post hook was not called")
	}
}

type toolExecutionHook struct {
	pre  func(context.Context, *hooks.PreToolCallContext) (hooks.PreToolCallResult, error)
	post func(context.Context, *hooks.PostToolCallContext)
}

func (toolExecutionHook) Name() string  { return "test" }
func (toolExecutionHook) Priority() int { return 0 }

func (h toolExecutionHook) OnPreToolCall(ctx context.Context, hctx *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
	return h.pre(ctx, hctx)
}

func (h toolExecutionHook) OnPostToolCall(ctx context.Context, hctx *hooks.PostToolCallContext) {
	h.post(ctx, hctx)
}
