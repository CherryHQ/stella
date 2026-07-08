package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
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
	if params.MaxTokens != defaultMaxTokens {
		t.Errorf("max_tokens = %d, want %d (default)", params.MaxTokens, defaultMaxTokens)
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

// Multi-action tools describe per-action parameters inside a oneOf. The SDK's
// typed schema only models properties/required/type, so oneOf must survive via
// ExtraFields or Claude sees a tool with no usable parameters.
func TestConvertToolsPreservesOneOf(t *testing.T) {
	// Mirror a toolgen schema decoded via json.Unmarshal: arrays are []any.
	tools := []ai.ToolDefinition{
		{
			Name: "goal",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"action": map[string]any{"enum": []any{"create", "list"}}},
				"required":   []any{"action"},
				"oneOf": []any{
					map[string]any{
						"properties": map[string]any{
							"action": map[string]any{"const": "create"},
							"title":  map[string]any{"type": "string"},
						},
						"required": []any{"action", "title"},
					},
				},
			},
		},
	}
	out := convertTools(tools)
	if len(out) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(out))
	}
	// Top-level required must survive the []any coercion.
	if got := out[0].OfTool.InputSchema.Required; len(got) != 1 || got[0] != "action" {
		t.Errorf("required = %v, want [action]", got)
	}
	// oneOf (with the create branch's title) must reach the wire.
	data, err := json.Marshal(out[0].OfTool.InputSchema)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"oneOf"`) {
		t.Errorf("serialized schema dropped oneOf: %s", data)
	}
	if !strings.Contains(string(data), `"title"`) {
		t.Errorf("serialized schema dropped per-action title: %s", data)
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
