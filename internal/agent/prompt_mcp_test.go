package agent

import (
	"context"
	"strings"
	"testing"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestBuildSystemPromptIncludesMCPTools(t *testing.T) {
	prompt := BuildSystemPromptFromDB(context.Background(), DBPromptParams{SystemPrompt: "You are Stella.", PromptTools: []pkgplugins.PromptToolInfo{{Name: "mcp__github__searchrepos", Description: "Search repositories", Metadata: map[string]any{"server_name": "github"}}}})
	if !strings.Contains(prompt, "`skills`: Load, search, install, list, remove, create, and update local skills") {
		t.Fatalf("expected builtin skills tool in prompt: %s", prompt)
	}
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
	prompt := BuildSystemPromptFromDB(context.Background(), DBPromptParams{SystemPrompt: "You are Stella."})
	if !strings.Contains(prompt, "`skills`: Load, search, install, list, remove, create, and update local skills") {
		t.Fatalf("expected builtin skills tool in prompt: %s", prompt)
	}
	if strings.Contains(prompt, "Proxy configured MCP tools") {
		t.Fatalf("did not expect MCP prompt section when manager disabled: %s", prompt)
	}
}

func TestBuildSystemPromptIncludesPromptSections(t *testing.T) {
	prompt := BuildSystemPromptFromDB(context.Background(), DBPromptParams{
		SystemPrompt: "You are Stella.",
		PromptSections: []pkgplugins.SystemPromptSection{
			{Title: "Skills", Content: "<available_skills>\n  <skill>demo</skill>\n</available_skills>"},
		},
	})
	if !strings.Contains(prompt, "## Skills") {
		t.Fatalf("expected prompt section title in prompt: %s", prompt)
	}
	if !strings.Contains(prompt, "<available_skills>") {
		t.Fatalf("expected prompt section content in prompt: %s", prompt)
	}
}
