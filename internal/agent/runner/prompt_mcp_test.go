package runner

import (
	"context"
	"strings"
	"testing"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func TestBuildSystemPromptIncludesMCPTools(t *testing.T) {
	prompt := BuildSystemPromptFromDB(context.Background(), DBPromptParams{SystemPrompt: "You are Anna.", PromptTools: []pkgplugins.PromptToolInfo{{Name: "mcp__github__searchrepos", Description: "Search repositories", Metadata: map[string]any{"server_name": "github"}}}})
	if !strings.Contains(prompt, "`mcp`: Proxy configured MCP tools") {
		t.Fatalf("expected MCP tool section in prompt: %s", prompt)
	}
	if !strings.Contains(prompt, "mcp__github__searchrepos") {
		t.Fatalf("expected canonical MCP tool ID in prompt: %s", prompt)
	}
	if !strings.Contains(prompt, "always call `mcp` with `action=\"get\"`") {
		t.Fatalf("expected get-before-exec instruction in prompt: %s", prompt)
	}
}

func TestBuildSystemPromptOmitsMCPToolsWhenDisabled(t *testing.T) {
	prompt := BuildSystemPromptFromDB(context.Background(), DBPromptParams{SystemPrompt: "You are Anna."})
	if strings.Contains(prompt, "Proxy configured MCP tools") {
		t.Fatalf("did not expect MCP prompt section when manager disabled: %s", prompt)
	}
}
