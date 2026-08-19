package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

// An agent's persona prompt names it, often with the product default, so two
// agents in one group can both believe they are "Stella". The group section
// must state which name in the transcript is this agent's own.
func TestGroupRosterNamesTheAgentAndItsPeers(t *testing.T) {
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Stella — a sharp, efficient personal AI assistant.",
		Memory:       memorytest.New(),
		AgentID:      "anna",
		GroupID:      "grp-1",
		GroupRoster:  prompt.GroupRoster{SelfName: "Anna", PeerNames: []string{"Stella", "Coder"}},
	})

	for _, want := range []string{"you are **@Anna**", "@Stella", "@Coder"} {
		if !strings.Contains(p, want) {
			t.Errorf("expected group prompt to contain %q\n---\n%s", want, p)
		}
	}
}

// Without a roster the section stays as it was: no empty "you are @" line.
func TestGroupPromptWithoutRosterOmitsIdentityLine(t *testing.T) {
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       memorytest.New(),
		AgentID:      "anna",
		GroupID:      "grp-1",
	})
	if strings.Contains(p, "In this group you are") {
		t.Errorf("expected no identity line without a roster\n---\n%s", p)
	}
	if !strings.Contains(p, "## Group Collaboration") {
		t.Errorf("expected the group section to still render\n---\n%s", p)
	}
}
