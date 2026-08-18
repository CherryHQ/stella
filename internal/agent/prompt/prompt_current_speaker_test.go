package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

func TestCurrentSpeakerNotRenderedInGroupSystemPrompt(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	_ = fake.SetProfile(ctx, "speaker1", "a1", "Alice likes tea")
	fake.AddProfileEntry("speaker1", "a1", memory.ProfileEntry{
		ID: "e1", Text: "Based in Berlin", CreatedAt: "2026-06-01T00:00:00Z",
	})
	// Another member's private profile must never leak into a group turn.
	_ = fake.SetProfile(ctx, "other", "a1", "OTHER MEMBER SECRET")

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       fake,
		UserID:       "",
		AgentID:      "a1",
		GroupID:      "grp-1",
	})

	for _, want := range []string{"## Group Conversation Recall", "memory.search"} {
		if !strings.Contains(p, want) {
			t.Errorf("expected prompt to contain %q\n---\n%s", want, p)
		}
	}
	for _, forbidden := range []string{"## Current Speaker", "Linked Stella user", "Alice likes tea", "Based in Berlin", "OTHER MEMBER SECRET", "<speaker_profile>"} {
		if strings.Contains(p, forbidden) {
			t.Errorf("group system prompt must not include per-turn/private speaker content %q", forbidden)
		}
	}
}

func TestCurrentSpeakerAbsentInDM(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	_ = fake.SetProfile(ctx, "u1", "a1", "DM profile")

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       fake,
		UserID:       "u1",
		AgentID:      "a1",
	})

	if strings.Contains(p, "## Current Speaker") {
		t.Error("DM turn must not render a Current Speaker section")
	}
	if !strings.Contains(p, "## User Profile") {
		t.Error("DM turn should render User Profile")
	}
}
