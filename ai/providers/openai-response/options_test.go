package openairesponse

import (
	"testing"

	"github.com/vaayne/anna/ai"
)

func TestBuildParamsBasic(t *testing.T) {
	model := ai.Model{Name: "gpt-4o"}
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.UserMessage{Content: "hi"},
		},
	}
	opts := ai.StreamOptions{}

	params := buildParams(model, ctx, opts)
	if string(params.Model) != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", params.Model)
	}
}

func TestBuildParamsWithSystem(t *testing.T) {
	model := ai.Model{Name: "gpt-4o"}
	ctx := ai.Context{
		System:   "you are a helper",
		Messages: []ai.Message{ai.UserMessage{Content: "hi"}},
	}
	opts := ai.StreamOptions{}

	params := buildParams(model, ctx, opts)
	if params.Instructions.Value == "" {
		t.Error("expected Instructions to be set")
	}
}

func TestBuildParamsWithTemperature(t *testing.T) {
	model := ai.Model{Name: "gpt-4o"}
	ctx := ai.Context{Messages: []ai.Message{ai.UserMessage{Content: "hi"}}}
	temp := 0.7
	opts := ai.StreamOptions{Temperature: &temp}

	params := buildParams(model, ctx, opts)
	if params.Temperature.Value != 0.7 {
		t.Errorf("temperature = %v, want 0.7", params.Temperature.Value)
	}
}

func TestBuildParamsWithMaxTokens(t *testing.T) {
	model := ai.Model{Name: "gpt-4o"}
	ctx := ai.Context{Messages: []ai.Message{ai.UserMessage{Content: "hi"}}}
	maxTok := 500
	opts := ai.StreamOptions{MaxTokens: &maxTok}

	params := buildParams(model, ctx, opts)
	if params.MaxOutputTokens.Value != 500 {
		t.Errorf("max_output_tokens = %d, want 500", params.MaxOutputTokens.Value)
	}
}

func TestBuildParamsWithTools(t *testing.T) {
	model := ai.Model{Name: "gpt-4o"}
	ctx := ai.Context{
		Messages: []ai.Message{ai.UserMessage{Content: "hi"}},
		Tools: []ai.ToolDefinition{
			{Name: "bash", Description: "run commands", InputSchema: map[string]any{"type": "object"}},
			{Name: "read", Description: "read files", InputSchema: map[string]any{"type": "object"}},
		},
	}
	opts := ai.StreamOptions{}

	params := buildParams(model, ctx, opts)
	if len(params.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(params.Tools))
	}
	if params.Tools[0].OfFunction == nil {
		t.Fatal("expected OfFunction")
	}
	if params.Tools[0].OfFunction.Name != "bash" {
		t.Errorf("tool name = %q, want bash", params.Tools[0].OfFunction.Name)
	}
}

func TestConvertTools(t *testing.T) {
	tools := []ai.ToolDefinition{
		{Name: "search", Description: "search things", InputSchema: map[string]any{"type": "object"}},
	}
	out := convertTools(tools)
	if len(out) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(out))
	}
	if out[0].OfFunction.Name != "search" {
		t.Errorf("name = %q, want search", out[0].OfFunction.Name)
	}
	if out[0].OfFunction.Description.Value != "search things" {
		t.Errorf("description = %q, want 'search things'", out[0].OfFunction.Description.Value)
	}
}

func TestConvertToolsEmpty(t *testing.T) {
	out := convertTools(nil)
	if len(out) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(out))
	}
}
