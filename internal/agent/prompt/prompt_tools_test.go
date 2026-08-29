package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
)

// TestSystemPromptDescribesCodeRouting pins the one tool section every session
// gets: the hot set is called directly, everything else goes through `code`.
func TestSystemPromptDescribesCodeRouting(t *testing.T) {
	system := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{SystemPrompt: "base"})

	for _, guidance := range []string{
		"`code`",
		"`bash`",
		"Native tools for standalone work",
		"Never wrap a standalone native call in `code`",
		"If the exact name is known but its schema is not, describe it directly",
		"Skill search finds behavior guides, not tools",
	} {
		if !strings.Contains(system, guidance) {
			t.Fatalf("prompt lost routing guidance %q", guidance)
		}
	}
}
