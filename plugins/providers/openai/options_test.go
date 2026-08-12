package openai

import (
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestBuildParamsBasic(t *testing.T) {
	model := ai.Model{Name: "gpt-4o"}
	ctx := ai.Context{
		Messages: []ai.Message{ai.UserMessage{Content: "hi"}},
	}
	opts := ai.StreamOptions{}

	params := buildParams(model, ctx, opts)
	if params.Model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", params.Model)
	}
	if len(params.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(params.Messages))
	}
	if !params.StreamOptions.IncludeUsage.Value {
		t.Error("stream_options.include_usage = false, want true")
	}
}

func TestBuildParamsWithTemperature(t *testing.T) {
	model := ai.Model{Name: "gpt-4o"}
	ctx := ai.Context{Messages: []ai.Message{ai.UserMessage{Content: "hi"}}}
	temp := 0.5
	opts := ai.StreamOptions{Temperature: &temp}

	params := buildParams(model, ctx, opts)
	if params.Temperature.Value != 0.5 {
		t.Errorf("temperature = %v, want 0.5", params.Temperature.Value)
	}
}

func TestBuildParamsWithMaxTokens(t *testing.T) {
	model := ai.Model{Name: "gpt-4o"}
	ctx := ai.Context{Messages: []ai.Message{ai.UserMessage{Content: "hi"}}}
	maxTok := 1000
	opts := ai.StreamOptions{MaxTokens: &maxTok}

	params := buildParams(model, ctx, opts)
	if params.MaxCompletionTokens.Value != 1000 {
		t.Errorf("max_completion_tokens = %d, want 1000", params.MaxCompletionTokens.Value)
	}
}

func TestBuildParamsWithTools(t *testing.T) {
	model := ai.Model{Name: "gpt-4o"}
	ctx := ai.Context{
		Messages: []ai.Message{ai.UserMessage{Content: "hi"}},
		Tools: []ai.ToolDefinition{
			{Name: "bash", Description: "run commands", InputSchema: map[string]any{"type": "object"}},
		},
	}
	opts := ai.StreamOptions{}

	params := buildParams(model, ctx, opts)
	if len(params.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(params.Tools))
	}
	if params.Tools[0].Function.Name != "bash" {
		t.Errorf("tool name = %q, want bash", params.Tools[0].Function.Name)
	}
}

func TestBuildParamsNoOptionalFields(t *testing.T) {
	model := ai.Model{Name: "gpt-4o"}
	ctx := ai.Context{Messages: []ai.Message{ai.UserMessage{Content: "hi"}}}
	opts := ai.StreamOptions{}

	params := buildParams(model, ctx, opts)
	if len(params.Tools) != 0 {
		t.Errorf("expected no tools, got %d", len(params.Tools))
	}
}

func TestConvertTools(t *testing.T) {
	tools := []ai.ToolDefinition{
		{Name: "search", Description: "find things", InputSchema: map[string]any{"type": "object"}},
		{Name: "write", Description: "write files", InputSchema: map[string]any{"type": "object"}},
	}
	out := convertTools(tools)
	if len(out) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(out))
	}
	if out[0].Function.Name != "search" {
		t.Errorf("tool[0] name = %q, want search", out[0].Function.Name)
	}
	if out[1].Function.Name != "write" {
		t.Errorf("tool[1] name = %q, want write", out[1].Function.Name)
	}
}

func TestConvertToolsEmpty(t *testing.T) {
	out := convertTools(nil)
	if len(out) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(out))
	}
}

func TestBuildRequestOptionsEmpty(t *testing.T) {
	opts := ai.StreamOptions{}
	reqOpts := buildRequestOptions(opts)
	if len(reqOpts) != 0 {
		t.Errorf("expected 0 request options, got %d", len(reqOpts))
	}
}

func TestBuildRequestOptionsAll(t *testing.T) {
	opts := ai.StreamOptions{
		Headers: map[string]string{"X-Custom": "val"},
	}
	reqOpts := buildRequestOptions(opts)
	if len(reqOpts) != 1 {
		t.Errorf("expected 1 request option, got %d", len(reqOpts))
	}
}
