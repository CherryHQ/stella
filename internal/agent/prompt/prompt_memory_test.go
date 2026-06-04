package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

func TestConstraintsDatesRendered(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	if _, err := fake.AddConstraint(ctx, "u1", "a1", "Always be polite"); err != nil {
		t.Fatal(err)
	}

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       fake,
		UserID:       "u1",
		AgentID:      "a1",
	})

	if !strings.Contains(p, "## Constraints") {
		t.Fatal("expected Constraints section")
	}
	if !strings.Contains(p, "Always be polite") {
		t.Fatal("expected constraint text")
	}
	// Date should be rendered in the constraint line (RFC3339 contains "T").
	constraintsIdx := strings.Index(p, "## Constraints")
	soulIdx := strings.Index(p, "## Agent Soul")
	constraintsSection := p[constraintsIdx:soulIdx]
	if !strings.Contains(constraintsSection, "T") || !strings.Contains(constraintsSection, "Z") {
		t.Errorf("expected RFC3339 date in constraints section, got:\n%s", constraintsSection)
	}
}

func TestProfileEntriesRendered(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	_ = fake.SetProfile(ctx, "u1", "a1", "Manual profile text")
	fake.AddProfileEntry("u1", "a1", memory.ProfileEntry{
		ID:        "pe1",
		Text:      "Prefers dark mode",
		Source:    "auto",
		CreatedAt: "2026-06-04T10:00:00Z",
	})

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       fake,
		UserID:       "u1",
		AgentID:      "a1",
	})

	if !strings.Contains(p, "Manual profile text") {
		t.Error("expected manual profile text in prompt")
	}
	if !strings.Contains(p, "Prefers dark mode") {
		t.Error("expected auto profile entry text in prompt")
	}
	if !strings.Contains(p, "2026-06-04T10:00:00Z") {
		t.Error("expected profile entry date in prompt")
	}
}

func TestOldProfileWithoutEntriesStillRenders(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	_ = fake.SetProfile(ctx, "u1", "a1", "Old undated profile blob")

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       fake,
		UserID:       "u1",
		AgentID:      "a1",
	})

	if !strings.Contains(p, "Old undated profile blob") {
		t.Error("expected old profile text to render")
	}
	if !strings.Contains(p, "User Profile") {
		t.Error("expected User Profile section")
	}
}

func TestGroupMemoryInjectedInsteadOfProfile(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	fake.SetGroupMemory("grp-123", "This group discusses Go programming.")
	_ = fake.SetProfile(ctx, "u1", "a1", "Should not appear in group sessions")

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       fake,
		UserID:       "",
		AgentID:      "a1",
		GroupID:      "grp-123",
	})

	if !strings.Contains(p, "Group Memory") {
		t.Error("expected Group Memory section for group session")
	}
	if !strings.Contains(p, "This group discusses Go programming.") {
		t.Error("expected group memory content in prompt")
	}
	if strings.Contains(p, "User Profile") {
		t.Error("group session should not have User Profile section")
	}
	if strings.Contains(p, "Should not appear") {
		t.Error("group session should not contain user's private profile")
	}
}

func TestGroupSessionWithEmptyGroupMemory(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       fake,
		UserID:       "",
		AgentID:      "a1",
		GroupID:      "grp-empty",
	})

	// With empty group memory, the template should fall through to the else branch
	// which shows User Profile. Since UserID is empty, profile will be empty.
	if strings.Contains(p, "Group Memory") {
		t.Error("empty group memory should not produce Group Memory section")
	}
}

func TestDMSessionDoesNotShowGroupMemory(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	_ = fake.SetProfile(ctx, "u1", "a1", "DM profile content")

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       fake,
		UserID:       "u1",
		AgentID:      "a1",
	})

	if strings.Contains(p, "Group Memory") {
		t.Error("DM session should not show Group Memory")
	}
	if !strings.Contains(p, "User Profile") {
		t.Error("DM session should show User Profile")
	}
	if !strings.Contains(p, "DM profile content") {
		t.Error("DM session should contain user profile content")
	}
}
