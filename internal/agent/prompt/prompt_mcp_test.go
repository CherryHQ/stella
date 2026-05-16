package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestBuildSystemPromptIncludesMCPTools(t *testing.T) {
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{SystemPrompt: "You are Stella.", PromptTools: []pkgplugins.PromptToolInfo{{Name: "mcp__github__searchrepos", Description: "Search repositories", Metadata: map[string]any{"server_name": "github"}}}})
	if !strings.Contains(p, "`memory`: Manage persistent knowledge") {
		t.Fatalf("expected builtin memory tool in prompt: %s", p)
	}
	if !strings.Contains(p, "`mcp`: Proxy configured MCP tools") {
		t.Fatalf("expected MCP tool section in prompt: %s", p)
	}
	if !strings.Contains(p, "mcp__github__searchrepos") {
		t.Fatalf("expected canonical MCP tool ID in prompt: %s", p)
	}
	if !strings.Contains(p, "always call `mcp` with `action=\"get\"`") {
		t.Fatalf("expected get-before-exec instruction in prompt: %s", p)
	}
}

func TestBuildSystemPromptOmitsMCPToolsWhenDisabled(t *testing.T) {
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{SystemPrompt: "You are Stella."})
	if !strings.Contains(p, "`memory`: Manage persistent knowledge") {
		t.Fatalf("expected builtin memory tool in prompt: %s", p)
	}
	if strings.Contains(p, "Proxy configured MCP tools") {
		t.Fatalf("did not expect MCP prompt section when manager disabled: %s", p)
	}
}

func TestBuildSystemPromptIncludesPromptSections(t *testing.T) {
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Sections: []pkgplugins.SystemPromptSection{
			{Title: "Skills", Content: "<available_skills>\n  <skill>demo</skill>\n</available_skills>"},
		},
	})
	if !strings.Contains(p, "## Skills") {
		t.Fatalf("expected prompt section title in prompt: %s", p)
	}
	if !strings.Contains(p, "<available_skills>") {
		t.Fatalf("expected prompt section content in prompt: %s", p)
	}
}
