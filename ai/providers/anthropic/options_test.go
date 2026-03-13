package anthropic

import (
	"testing"

	"github.com/vaayne/anna/ai"
)

func TestBuildParamsBasic(t *testing.T) {
	model := ai.Model{Name: "claude-sonnet-4-20250514"}
	ctx := ai.Context{
		Messages: []ai.Message{ai.UserMessage{Content: "hi"}},
	}
	opts := ai.StreamOptions{}

	params := buildParams(model, ctx, opts)
	if string(params.Model) != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want claude-sonnet-4-20250514", params.Model)
	}
	if params.MaxTokens != 1024 {
		t.Errorf("max_tokens = %d, want 1024 (default)", params.MaxTokens)
	}
}

func TestBuildParamsWithSystem(t *testing.T) {
	model := ai.Model{Name: "claude-sonnet-4-20250514"}
	ctx := ai.Context{
		System:   "you are a helper",
		Messages: []ai.Message{ai.UserMessage{Content: "hi"}},
	}
	opts := ai.StreamOptions{}

	params := buildParams(model, ctx, opts)
	if len(params.System) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(params.System))
	}
	if params.System[0].Text != "you are a helper" {
		t.Errorf("system text = %q", params.System[0].Text)
	}
}

func TestBuildParamsWithMaxTokens(t *testing.T) {
	model := ai.Model{Name: "claude-sonnet-4-20250514"}
	ctx := ai.Context{Messages: []ai.Message{ai.UserMessage{Content: "hi"}}}
	maxTok := 2048
	opts := ai.StreamOptions{MaxTokens: &maxTok}

	params := buildParams(model, ctx, opts)
	if params.MaxTokens != 2048 {
		t.Errorf("max_tokens = %d, want 2048", params.MaxTokens)
	}
}

func TestBuildParamsWithTemperature(t *testing.T) {
	model := ai.Model{Name: "claude-sonnet-4-20250514"}
	ctx := ai.Context{Messages: []ai.Message{ai.UserMessage{Content: "hi"}}}
	temp := 0.3
	opts := ai.StreamOptions{Temperature: &temp}

	params := buildParams(model, ctx, opts)
	if params.Temperature.Value != 0.3 {
		t.Errorf("temperature = %v, want 0.3", params.Temperature.Value)
	}
}

func TestBuildParamsWithTools(t *testing.T) {
	model := ai.Model{Name: "claude-sonnet-4-20250514"}
	ctx := ai.Context{
		Messages: []ai.Message{ai.UserMessage{Content: "hi"}},
		Tools: []ai.ToolDefinition{
			{
				Name:        "bash",
				Description: "run commands",
				InputSchema: map[string]any{
					"properties": map[string]any{"command": map[string]any{"type": "string"}},
					"required":   []string{"command"},
				},
			},
		},
	}
	opts := ai.StreamOptions{}

	params := buildParams(model, ctx, opts)
	if len(params.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(params.Tools))
	}
	if params.Tools[0].OfTool == nil {
		t.Fatal("expected OfTool")
	}
	if params.Tools[0].OfTool.Name != "bash" {
		t.Errorf("tool name = %q, want bash", params.Tools[0].OfTool.Name)
	}
}

func TestBuildParamsNoSystem(t *testing.T) {
	model := ai.Model{Name: "claude-sonnet-4-20250514"}
	ctx := ai.Context{Messages: []ai.Message{ai.UserMessage{Content: "hi"}}}
	opts := ai.StreamOptions{}

	params := buildParams(model, ctx, opts)
	if len(params.System) != 0 {
		t.Errorf("expected no system blocks, got %d", len(params.System))
	}
}

func TestConvertTools(t *testing.T) {
	tools := []ai.ToolDefinition{
		{
			Name:        "search",
			Description: "search things",
			InputSchema: map[string]any{
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
			},
		},
	}
	out := convertTools(tools)
	if len(out) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(out))
	}
	if out[0].OfTool.Name != "search" {
		t.Errorf("name = %q, want search", out[0].OfTool.Name)
	}
}

func TestConvertToolsEmpty(t *testing.T) {
	out := convertTools(nil)
	if len(out) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(out))
	}
}

func TestConvertToolsWithRequired(t *testing.T) {
	tools := []ai.ToolDefinition{
		{
			Name:        "bash",
			Description: "run commands",
			InputSchema: map[string]any{
				"properties": map[string]any{"command": map[string]any{"type": "string"}},
				"required":   []string{"command"},
			},
		},
	}
	out := convertTools(tools)
	if len(out) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(out))
	}
	if len(out[0].OfTool.InputSchema.Required) != 1 {
		t.Errorf("expected 1 required field, got %d", len(out[0].OfTool.InputSchema.Required))
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
		APIKey:  "key",
		BaseURL: "https://example.com",
		Headers: map[string]string{"X-Custom": "val"},
	}
	reqOpts := buildRequestOptions(opts)
	if len(reqOpts) != 3 {
		t.Errorf("expected 3 request options, got %d", len(reqOpts))
	}
}

func TestMapStopReasonValues(t *testing.T) {
	tests := []struct {
		reason string
		want   string
	}{
		{"tool_use", "toolUse"},
		{"max_tokens", "length"},
		{"end_turn", "stop"},
		{"unknown", "stop"},
	}
	for _, tt := range tests {
		got := mapStopReason(tt.reason)
		if string(got) != tt.want {
			t.Errorf("mapStopReason(%q) = %q, want %q", tt.reason, got, tt.want)
		}
	}
}
