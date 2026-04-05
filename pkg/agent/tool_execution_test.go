package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/hooks"
)

func TestToolExecution(t *testing.T) {
	calls := []ai.ToolCall{{ID: "1", Name: "echo"}, {ID: "2", Name: "missing"}}
	tools := ToolSet{
		"echo": func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error) {
			return ai.TextContent{Text: "ok"}, nil
		},
	}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, nil, hooks.HookMeta{})
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
		"fail": func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error) {
			return ai.TextContent{}, errors.New("boom")
		},
	}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, nil, hooks.HookMeta{})
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
		"bash": func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error) {
			return ai.TextContent{Text: "pip: command not found"}, errors.New("bash: exit code 127")
		},
	}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, nil, hooks.HookMeta{})
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
		"fail": func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error) {
			return ai.TextContent{Text: ""}, errors.New("boom")
		},
	}

	results, err := executeToolCalls(context.Background(), calls, tools, toolCallbacks{}, nil, hooks.HookMeta{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := results[0].Content[0].(ai.TextContent).Text
	if text != "boom" {
		t.Errorf("with empty content, should just show error, got: %q", text)
	}
}
