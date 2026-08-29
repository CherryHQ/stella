package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
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

func TestToolExecutionPreservesImagePolicyWhenHookReplacesContext(t *testing.T) {
	type hookContextKey struct{}

	ctx := pkgtools.WithParentImageCapability(context.Background(), ai.ImageSupported)
	hs := hooks.NewHookSet([]hooks.HookPlugin{toolExecutionHook{
		pre: func(context.Context, *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
			return hooks.PreToolCallResult{Context: context.WithValue(context.Background(), hookContextKey{}, "hook")}, nil
		},
		post: func(context.Context, *hooks.PostToolCallContext) {},
	}})
	tools := ToolSet{"view_image": func(ctx context.Context, _ ai.ToolCall) ([]ai.ContentBlock, error) {
		if got := ctx.Value(hookContextKey{}); got != "hook" {
			t.Fatalf("hook context value = %v, want hook", got)
		}
		if got := pkgtools.ParentImageCapabilityFromContext(ctx); got != ai.ImageSupported {
			t.Fatalf("parent image capability = %v, want supported", got)
		}
		return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
	}}

	_, err := executeToolCalls(ctx, []ai.ToolCall{{ID: "1", Name: "view_image"}}, tools, toolCallbacks{}, hs, hooks.HookMeta{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
	if !results[0].IsError {
		t.Fatal("hook-blocked result must be an error")
	}
}

func TestToolExecutionLifecycleBlockIsError(t *testing.T) {
	results, err := executeToolCalls(context.Background(), []ai.ToolCall{{ID: "1", Name: "echo"}}, ToolSet{
		"echo": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			t.Fatal("lifecycle-blocked tool should not execute")
			return nil, nil
		},
	}, toolCallbacks{}, nil, hooks.HookMeta{}, &ToolLifecycle{
		BeforeCall: func(context.Context, ToolCallContext) (ToolCallMutation, error) {
			return ToolCallMutation{Block: true, BlockMessage: "blocked by lifecycle"}, nil
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("lifecycle-blocked result = %#v, want IsError", results)
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

// A command that ran and exited nonzero is the sandbox answering, not the tool
// breaking. The distinction has to survive as a field: every consumer that
// tried to recover it from the message text got it wrong.
func TestToolExecutionClassifiesCommandExitSeparately(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "bash"}, {ID: "2", Name: "edit"}}
	tools := ToolSet{
		"bash": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.TextContent{Text: "no such file"}}, &ai.CommandExitError{Tool: "bash", ExitCode: 2}
		},
		"edit": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return nil, errors.New("old_string not found")
		},
	}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, nil, hooks.HookMeta{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !results[0].IsError || results[0].ErrorKind != ai.ToolErrorKindCommandNonzero {
		t.Errorf("nonzero exit = (%v, %q), want (true, %q)", results[0].IsError, results[0].ErrorKind, ai.ToolErrorKindCommandNonzero)
	}
	if !results[1].IsError || results[1].ErrorKind != ai.ToolErrorKindTool {
		t.Errorf("tool failure = (%v, %q), want (true, %q)", results[1].IsError, results[1].ErrorKind, ai.ToolErrorKindTool)
	}
}

func TestToolExecutionClassifiesExplicitCommandTimeout(t *testing.T) {
	results, err := executeToolCalls(context.Background(), []ai.ToolCall{{ID: "1", Name: "bash"}}, ToolSet{
		"bash": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return nil, &ai.CommandTimeoutError{Tool: "bash"}
		},
	}, toolCallbacks{}, nil, hooks.HookMeta{}, nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(results) != 1 || results[0].ErrorKind != ai.ToolErrorKindCommandTimeout {
		t.Fatalf("timeout result = %#v, want command_timeout", results)
	}
}

// A tool that never failed carries no kind: an empty kind on a successful
// result is the only value that cannot be misread as a claim.
func TestToolExecutionLeavesSuccessUnclassified(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "echo"}}
	tools := ToolSet{"echo": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
	}}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, nil, hooks.HookMeta{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results[0].ErrorKind != "" {
		t.Errorf("successful result carries kind %q", results[0].ErrorKind)
	}
}

// The span for a tool call is built from the PostToolCall payload, so the
// #1077 classification has to reach it — and with the command's exit status,
// which is the one number that says what the sandbox actually answered.
func TestToolExecutionReportsErrorKindToHooks(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "bash"}, {ID: "2", Name: "edit"}, {ID: "3", Name: "echo"}}
	tools := ToolSet{
		"bash": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return nil, &ai.CommandExitError{Tool: "bash", ExitCode: 2}
		},
		"edit": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return nil, errors.New("old_string not found")
		},
		"echo": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
			return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
		},
	}

	seen := map[string]hooks.PostToolCallContext{}
	hook := toolExecutionHook{
		pre: func(_ context.Context, _ *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
			return hooks.PreToolCallResult{}, nil
		},
		post: func(_ context.Context, hctx *hooks.PostToolCallContext) { seen[hctx.ToolCallID] = *hctx },
	}
	hs := hooks.NewHookSet([]hooks.HookPlugin{hook})

	if _, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, hs, hooks.HookMeta{}, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := seen["1"]; got.ErrorKind != ai.ToolErrorKindCommandNonzero || got.ExitCode == nil || *got.ExitCode != 2 {
		t.Errorf("nonzero exit reported kind %q exit %v, want command_nonzero exit 2", got.ErrorKind, got.ExitCode)
	}
	if got := seen["2"]; got.ErrorKind != ai.ToolErrorKindTool || got.ExitCode != nil {
		t.Errorf("tool failure reported kind %q exit %v, want tool_error and no exit code", got.ErrorKind, got.ExitCode)
	}
	if got := seen["3"]; got.ErrorKind != "" || got.ExitCode != nil {
		t.Errorf("success reported kind %q exit %v, want neither", got.ErrorKind, got.ExitCode)
	}
}

func TestToolExecutionEmitsCompletedResultBeforeLaterFailure(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "ok"}, {ID: "2", Name: "ok"}}
	tools := ToolSet{"ok": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		return []ai.ContentBlock{ai.TextContent{Text: "done"}}, nil
	}}
	lifecycle := &ToolLifecycle{BeforeCall: func(_ context.Context, call ToolCallContext) (ToolCallMutation, error) {
		if call.ToolCallID == "2" {
			return ToolCallMutation{}, errors.New("second call failed")
		}
		return ToolCallMutation{}, nil
	}}
	var events []string

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{
		onStart:  func(call ai.ToolCall) { events = append(events, "start "+call.ID) },
		onFinish: func(result ai.ToolResultMessage) { events = append(events, "finish "+result.ToolCallID) },
	}, nil, hooks.HookMeta{}, lifecycle, nil)
	if err == nil || err.Error() != "second call failed" {
		t.Fatalf("error = %v, want second call failed", err)
	}
	if len(results) != 1 || results[0].ToolCallID != "1" {
		t.Fatalf("completed results = %#v, want call 1", results)
	}
	if got, want := strings.Join(events, ","), "start 1,finish 1,start 2"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestToolExecutionEndsHookSpanBeforeFinishAndOnLaterFailure(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "ok"}, {ID: "2", Name: "ok"}}
	tools := ToolSet{"ok": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		return []ai.ContentBlock{ai.TextContent{Text: "done"}}, nil
	}}
	var events []string
	hs := hooks.NewHookSet([]hooks.HookPlugin{toolExecutionHook{
		pre: func(_ context.Context, hctx *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
			events = append(events, "span start "+hctx.ToolCallID)
			return hooks.PreToolCallResult{}, nil
		},
		post: func(_ context.Context, hctx *hooks.PostToolCallContext) {
			events = append(events, "span end "+hctx.ToolCallID)
		},
	}})
	lifecycle := &ToolLifecycle{AfterCall: func(_ context.Context, result ToolResultContext) (ToolResultMutation, error) {
		if result.ToolCallID == "2" {
			return ToolResultMutation{}, errors.New("second after failed")
		}
		return ToolResultMutation{}, nil
	}}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{
		onStart:  func(call ai.ToolCall) { events = append(events, "start "+call.ID) },
		onFinish: func(result ai.ToolResultMessage) { events = append(events, "finish "+result.ToolCallID) },
	}, hs, hooks.HookMeta{}, lifecycle, nil)
	if err == nil || err.Error() != "second after failed" {
		t.Fatalf("error = %v, want second after failed", err)
	}
	if len(results) != 1 || results[0].ToolCallID != "1" {
		t.Fatalf("completed results = %#v, want call 1", results)
	}
	want := "start 1,span start 1,span end 1,finish 1,start 2,span start 2,span end 2"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}
