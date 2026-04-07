package runner

import (
	"context"
	"strings"
	"testing"

	annamcp "github.com/vaayne/anna/internal/mcp"
)

func TestBuildSystemPromptIncludesMCPTools(t *testing.T) {
	mgr := annamcp.NewManager()
	mgr.Configure(annamcp.Config{}, true)
	mgr.RegisterTool("github", "search_repos", "Search Repos", "Search repositories", map[string]any{"type": "object"}, nil, nil)
	annamcp.SetDefaultManager(mgr)
	t.Cleanup(func() { annamcp.SetDefaultManager(annamcp.NewManager()) })

	prompt := BuildSystemPromptFromDB(context.Background(), DBPromptParams{SystemPrompt: "You are Anna."})
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
	annamcp.SetDefaultManager(annamcp.NewManager())
	prompt := BuildSystemPromptFromDB(context.Background(), DBPromptParams{SystemPrompt: "You are Anna."})
	if strings.Contains(prompt, "Proxy configured MCP tools") {
		t.Fatalf("did not expect MCP prompt section when manager disabled: %s", prompt)
	}
}
