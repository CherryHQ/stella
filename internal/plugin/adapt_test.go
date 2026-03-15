package plugin

import (
	"context"
	"testing"

	pluginapi "github.com/vaayne/anna/pkg/plugin"
)

func TestAdaptToolDefinition(t *testing.T) {
	pt := pluginapi.Tool{
		Name:        "search",
		Description: "Search for things",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
		},
	}

	adapted := AdaptTool(pt)
	def := adapted.Definition()

	if def.Name != "search" {
		t.Errorf("Name = %q, want %q", def.Name, "search")
	}
	if def.Description != "Search for things" {
		t.Errorf("Description = %q, want %q", def.Description, "Search for things")
	}
	if def.InputSchema == nil {
		t.Fatal("InputSchema should not be nil")
	}
	if def.InputSchema["type"] != "object" {
		t.Errorf("InputSchema[type] = %v, want %q", def.InputSchema["type"], "object")
	}
}

func TestAdaptToolExecute(t *testing.T) {
	var receivedArgs map[string]any
	pt := pluginapi.Tool{
		Name: "echo",
		Execute: func(_ context.Context, args map[string]any) (string, error) {
			receivedArgs = args
			return "echoed: " + args["msg"].(string), nil
		},
	}

	adapted := AdaptTool(pt)
	args := map[string]any{"msg": "hello"}
	result, err := adapted.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "echoed: hello" {
		t.Errorf("result = %q, want %q", result, "echoed: hello")
	}
	if receivedArgs["msg"] != "hello" {
		t.Errorf("args not passed correctly: %v", receivedArgs)
	}
}
