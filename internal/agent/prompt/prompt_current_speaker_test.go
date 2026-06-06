package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

func TestCurrentSpeakerLinkedRendersWithGroupMemory(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	fake.SetGroupMemory("grp-1", "This group talks about Go.")
	_ = fake.SetProfile(ctx, "speaker1", "a1", "Alice likes tea")
	fake.AddProfileEntry("speaker1", "a1", memory.ProfileEntry{
		ID: "e1", Text: "Based in Berlin", CreatedAt: "2026-06-01T00:00:00Z",
	})
	// Another member's private profile must never leak into a group turn.
	_ = fake.SetProfile(ctx, "other", "a1", "OTHER MEMBER SECRET")

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt:   "You are Stella.",
		Memory:         fake,
		UserID:         "",
		AgentID:        "a1",
		GroupID:        "grp-1",
		CurrentSpeaker: &memory.CurrentSpeaker{Platform: "telegram", DisplayName: "Alice", UserID: "speaker1"},
	})

	for _, want := range []string{"## Group Memory", "## Current Speaker", "Alice", "Alice likes tea", "Based in Berlin"} {
		if !strings.Contains(p, want) {
			t.Errorf("expected prompt to contain %q\n---\n%s", want, p)
		}
	}
	if strings.Contains(p, "## User Profile") {
		t.Error("group turn must not render the per-user User Profile section")
	}
	if strings.Contains(p, "OTHER MEMBER SECRET") {
		t.Error("group turn must not leak another member's profile")
	}
}

func TestCurrentSpeakerUnlinkedRendersNameOnly(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	// An unlinked sender that happens to share a platform id with a real user's
	// profile must NOT pull that profile — only UserID drives the lookup.
	_ = fake.SetProfile(ctx, "tg-stranger", "a1", "WRONG PROFILE")

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt:   "You are Stella.",
		Memory:         fake,
		AgentID:        "a1",
		GroupID:        "grp-1",
		CurrentSpeaker: &memory.CurrentSpeaker{Platform: "telegram", PlatformUserID: "tg-stranger", DisplayName: "Stranger"},
	})

	if !strings.Contains(p, "## Current Speaker") {
		t.Error("expected Current Speaker section for unlinked sender")
	}
	if !strings.Contains(p, "Stranger") {
		t.Error("expected unlinked sender display name")
	}
	if !strings.Contains(p, "Unlinked sender") {
		t.Error("expected unlinked marker")
	}
	if strings.Contains(p, "WRONG PROFILE") {
		t.Error("unlinked sender must not resolve any profile")
	}
}

func TestCurrentSpeakerSoulAndConstraintsNotInjected(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	_ = fake.SetAgentSoul(ctx, "speaker1", "a1", "SPEAKER SECRET SOUL")
	if _, err := fake.AddConstraint(ctx, "speaker1", "a1", "SPEAKER SECRET CONSTRAINT"); err != nil {
		t.Fatal(err)
	}
	_ = fake.SetProfile(ctx, "speaker1", "a1", "Alice profile")

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt:   "You are Stella.",
		Memory:         fake,
		AgentID:        "a1",
		GroupID:        "grp-1",
		CurrentSpeaker: &memory.CurrentSpeaker{DisplayName: "Alice", UserID: "speaker1"},
	})

	if !strings.Contains(p, "Alice profile") {
		t.Error("expected speaker profile to render")
	}
	if strings.Contains(p, "SPEAKER SECRET SOUL") {
		t.Error("speaker soul must not be injected into a group prompt")
	}
	if strings.Contains(p, "SPEAKER SECRET CONSTRAINT") {
		t.Error("speaker constraints must not be injected into a group prompt")
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
