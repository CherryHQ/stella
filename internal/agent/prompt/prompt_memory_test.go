package prompt_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type fakeKnowledgeStore struct {
	entries []pkgplugins.KnowledgeEntry
	onList  func(pkgplugins.SkillViewContext)
}

func (s fakeKnowledgeStore) ListKnowledge(_ context.Context, vc pkgplugins.SkillViewContext, _ ...pkgplugins.KnowledgeType) ([]pkgplugins.KnowledgeEntry, error) {
	if s.onList != nil {
		s.onList(vc)
	}
	return append([]pkgplugins.KnowledgeEntry(nil), s.entries...), nil
}

func (s fakeKnowledgeStore) ExpireKnowledgeDraftsByType(context.Context, pkgplugins.KnowledgeType, time.Time) error {
	return nil
}

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

	// Group mode is keyed on GroupID, not on group memory being non-empty: a group
	// turn renders Group Memory (empty) and never falls back to the per-user
	// User Profile section (D9 — issue #308).
	if !strings.Contains(p, "Group Memory") {
		t.Error("group session should render Group Memory even when empty")
	}
	if strings.Contains(p, "User Profile") {
		t.Error("group session must never render User Profile, even with empty group memory")
	}
}

func TestGroupSessionInjectsAgentScopedKnowledgeWithoutUser(t *testing.T) {
	ctx := context.Background()
	var sawList bool
	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		UserID:       "",
		AgentID:      "a1",
		GroupID:      "grp-knowledge",
		KnowledgeStore: fakeKnowledgeStore{
			entries: []pkgplugins.KnowledgeEntry{{
				Name:          "agent-policy",
				Content:       "System agent knowledge remains visible in groups.",
				KnowledgeType: pkgplugins.KnowledgeTypeFact,
			}},
			onList: func(vc pkgplugins.SkillViewContext) {
				sawList = true
				if vc.UserID != "" {
					t.Fatalf("group knowledge lookup user id = %q, want empty", vc.UserID)
				}
				if vc.AgentID != "a1" {
					t.Fatalf("group knowledge lookup agent id = %q, want a1", vc.AgentID)
				}
			},
		},
	})

	if !sawList {
		t.Fatal("expected group prompt to query knowledge store")
	}
	if !strings.Contains(p, "System agent knowledge remains visible in groups.") {
		t.Fatalf("expected group prompt to include agent knowledge: %s", p)
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

func TestKnowledgeEntriesRenderedAndSnapshotFiltered(t *testing.T) {
	ctx := context.Background()
	cutoff := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		UserID:       "u1",
		AgentID:      "a1",
		KnowledgeStore: fakeKnowledgeStore{entries: []pkgplugins.KnowledgeEntry{
			{
				Name:          "database",
				Content:       "Stella uses PostgreSQL.",
				KnowledgeType: pkgplugins.KnowledgeTypeFact,
				UpdatedAt:     cutoff.Add(-time.Minute),
			},
			{
				Name:          "future",
				Content:       "This should not appear.",
				KnowledgeType: pkgplugins.KnowledgeTypeContext,
				UpdatedAt:     cutoff.Add(time.Minute),
			},
		}},
		SnapshotVersion:   1,
		SnapshotUpdatedAt: cutoff,
	})

	if !strings.Contains(p, "## Knowledge") {
		t.Fatal("expected Knowledge section")
	}
	if !strings.Contains(p, "Stella uses PostgreSQL.") {
		t.Fatal("expected active knowledge content")
	}
	if strings.Contains(p, "This should not appear.") {
		t.Fatal("expected snapshot filter to remove future knowledge")
	}
}
