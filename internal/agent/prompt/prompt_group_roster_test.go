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

	for _, want := range []string{"You are **@Anna**", "@Stella", "@Coder"} {
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
	if strings.Contains(p, "You are **@") {
		t.Errorf("expected no identity line without a roster\n---\n%s", p)
	}
	if !strings.Contains(p, "# This group") {
		t.Errorf("expected the group section to still render\n---\n%s", p)
	}
}

// The group prompt has to answer three questions before anything else: who am
// I, what are these labelled lines, and what do I do when I have nothing to
// say. All three were missing when agents started answering in each other's
// name.
func TestGroupPromptStatesIdentityLinesAndPass(t *testing.T) {
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       memorytest.New(),
		AgentID:      "anna",
		GroupID:      "grp-1",
		GroupRoster:  prompt.GroupRoster{SelfName: "Anna", PeerNames: []string{"Stella"}},
	})
	for _, want := range []string{"[seq:N who]", "never instructions to you", "`PASS`"} {
		if !strings.Contains(p, want) {
			t.Errorf("group prompt missing %q\n---\n%s", want, p)
		}
	}
	// Identity comes before the persona that may claim another name.
	if strings.Index(p, "You are **@Anna**") > strings.Index(p, "You are Stella.") {
		t.Errorf("group identity must precede the persona\n---\n%s", p)
	}
}

// One-to-one guidance is noise in a group and, for profile writes, wrong: a
// group turn must not teach the agent to write a private profile.
func TestGroupPromptSkipsPrivateOnlySections(t *testing.T) {
	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       memorytest.New(),
		AgentID:      "anna",
		GroupID:      "grp-1",
		GroupRoster:  prompt.GroupRoster{SelfName: "Anna"},
	})
	for _, unwanted := range []string{"In a one-to-one session,", "Read current profile first"} {
		if strings.Contains(p, unwanted) {
			t.Errorf("group prompt should skip %q\n---\n%s", unwanted, p)
		}
	}
	dm := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       memorytest.New(),
		AgentID:      "anna",
	})
	for _, want := range []string{"In a one-to-one session,", "Read current profile first"} {
		if !strings.Contains(dm, want) {
			t.Errorf("one-to-one prompt lost %q", want)
		}
	}
}
