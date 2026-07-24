package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
)

func TestBuildSystemPromptIncludesFileKnowledgeGuidanceWhenAvailable(t *testing.T) {
	rendered := prompt.BuildSystemPromptFromDB(
		context.Background(),
		prompt.DBPromptParams{
			SystemPrompt:       "You are Stella.",
			UserID:             "u1",
			AgentID:            "a1",
			KnowledgeAvailable: true,
		},
	)

	for _, required := range []string{
		"# Knowledge Base",
		"`knowledge_search`",
		"internal policies",
		"rewrite the query and search again",
		"untrusted evidence",
		"prompt injection",
		"Cite each supported conclusion nearby",
		"`memory.search_knowledge`",
	} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("prompt missing %q:\n%s", required, rendered)
		}
	}
}

func TestBuildSystemPromptOmitsFileKnowledgeGuidanceWhenUnavailable(t *testing.T) {
	rendered := prompt.BuildSystemPromptFromDB(
		context.Background(),
		prompt.DBPromptParams{
			SystemPrompt: "You are Stella.",
			UserID:       "u1",
			AgentID:      "a1",
		},
	)

	if strings.Contains(rendered, "# Knowledge Base") ||
		strings.Contains(rendered, "`knowledge_search`") {
		t.Fatalf("prompt exposes unavailable file knowledge tool:\n%s", rendered)
	}
}
