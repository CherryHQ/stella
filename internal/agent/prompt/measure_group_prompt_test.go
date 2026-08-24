package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

// groupPromptCeiling is a ratchet, not a budget: the group prompt renders at
// ~6.6k characters today. Everything above this line was kept because cutting
// it would drop guidance the agent still needs in a group; raising the ceiling
// should take an argument about what the model gains.
const groupPromptCeiling = 7000

func TestGroupPromptStaysUnderCeiling(t *testing.T) {
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Anna — a sharp, efficient personal AI assistant.",
		Memory:       memorytest.New(),
		AgentID:      "anna",
		GroupID:      "grp-1",
		GroupRoster:  prompt.GroupRoster{SelfName: "Anna", PeerNames: []string{"Stella"}},
	})
	if len(p) > groupPromptCeiling {
		t.Fatalf("group prompt = %d chars, ceiling %d", len(p), groupPromptCeiling)
	}
	// The identity block is the part that has to survive any future trimming.
	if !strings.HasPrefix(p, "# This group") {
		t.Fatalf("group prompt no longer opens with identity:\n%s", p[:200])
	}
}
