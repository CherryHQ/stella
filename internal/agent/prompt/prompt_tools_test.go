package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
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

// A group turn used to be the one session shape that could route tools
// differently. It no longer is: the group prompt carries the same routing
// section as a direct chat, so a member agent reaches cold tools the same way.
func TestGroupPromptDescribesCodeRouting(t *testing.T) {
	system := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "base",
		Memory:       memorytest.New(),
		AgentID:      "anna",
		GroupID:      "grp-1",
		GroupRoster:  prompt.GroupRoster{SelfName: "Anna", PeerNames: []string{"Stella"}},
	})

	for _, guidance := range []string{
		"`code`",
		"`bash`",
		"Native tools for standalone work",
		"Never wrap a standalone native call in `code`",
	} {
		if !strings.Contains(system, guidance) {
			t.Fatalf("group prompt lost routing guidance %q", guidance)
		}
	}
}
