package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

type snapshotPromptMemory struct {
	*memorytest.Fake
	currentReads      int
	versionedVersions []int64
}

func (m *snapshotPromptMemory) GetAgentSoul(context.Context, string, string) (string, error) {
	m.currentReads++
	return "live-sentinel-soul", nil
}

func (m *snapshotPromptMemory) GetProfile(context.Context, string, string) (string, error) {
	m.currentReads++
	return "live-sentinel-profile", nil
}

func (m *snapshotPromptMemory) GetConstraints(context.Context, string, string) ([]memory.ConstraintEntry, error) {
	m.currentReads++
	return []memory.ConstraintEntry{{Text: "live-sentinel-constraint"}}, nil
}

func (m *snapshotPromptMemory) GetAgentSoulAt(_ context.Context, _ string, _ string, version int64) (string, error) {
	m.versionedVersions = append(m.versionedVersions, version)
	return "", nil
}

func (m *snapshotPromptMemory) GetProfileAt(_ context.Context, _ string, _ string, version int64) (string, error) {
	m.versionedVersions = append(m.versionedVersions, version)
	return "frozen profile", nil
}

func (m *snapshotPromptMemory) GetConstraintsAt(_ context.Context, _ string, _ string, version int64) ([]memory.ConstraintEntry, error) {
	m.versionedVersions = append(m.versionedVersions, version)
	return []memory.ConstraintEntry{{Text: "frozen constraint"}}, nil
}

func TestBuildSystemPromptUsesFrozenVersionZero(t *testing.T) {
	version := int64(0)
	mem := &snapshotPromptMemory{Fake: memorytest.New()}

	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt:    "You are Stella.",
		AgentSoul:       "agent default soul",
		Memory:          mem,
		UserID:          "u1",
		AgentID:         "a1",
		SnapshotVersion: &version,
	})

	if len(mem.versionedVersions) != 3 {
		t.Fatalf("versioned reads = %d, want 3", len(mem.versionedVersions))
	}
	for _, got := range mem.versionedVersions {
		if got != version {
			t.Fatalf("versioned read version = %d, want %d", got, version)
		}
	}
	if mem.currentReads != 0 {
		t.Fatalf("current reads = %d, want 0", mem.currentReads)
	}
	if !strings.Contains(p, "agent default soul") {
		t.Fatalf("expected agent default soul fallback in prompt:\n%s", p)
	}
	if !strings.Contains(p, "frozen profile") || !strings.Contains(p, "frozen constraint") {
		t.Fatalf("expected version-zero memory in prompt:\n%s", p)
	}
	if strings.Contains(p, "live-sentinel-soul") || strings.Contains(p, "live-sentinel-profile") || strings.Contains(p, "live-sentinel-constraint") {
		t.Fatalf("prompt used current memory instead of version zero:\n%s", p)
	}
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

func TestGroupSessionUsesPublicRecallGuidanceInsteadOfPrivateProfile(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	_ = fake.SetProfile(ctx, "u1", "a1", "Should not appear in group sessions")

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       fake,
		UserID:       "",
		AgentID:      "a1",
		GroupID:      "grp-123",
	})

	if !strings.Contains(p, "Group Conversation Recall") || !strings.Contains(p, "memory.search") {
		t.Error("expected public-history recall guidance for group session")
	}
	if strings.Contains(p, "User Profile") {
		t.Error("group session should not have User Profile section")
	}
	if strings.Contains(p, "Should not appear") {
		t.Error("group session should not contain user's private profile")
	}
}

func TestGroupSessionNeverFallsBackToUserProfile(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       fake,
		UserID:       "",
		AgentID:      "a1",
		GroupID:      "grp-empty",
	})

	// Group mode is keyed on GroupID and never falls back to the per-user profile.
	if !strings.Contains(p, "Group Conversation Recall") {
		t.Error("group session should render public-history recall guidance")
	}
	if strings.Contains(p, "User Profile") {
		t.Error("group session must never render User Profile")
	}
}

func TestDMSessionDoesNotShowGroupRecallGuidance(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	_ = fake.SetProfile(ctx, "u1", "a1", "DM profile content")

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       fake,
		UserID:       "u1",
		AgentID:      "a1",
	})

	if strings.Contains(p, "Group Conversation Recall") {
		t.Error("DM session should not show group recall guidance")
	}
	if !strings.Contains(p, "User Profile") {
		t.Error("DM session should show User Profile")
	}
	if !strings.Contains(p, "DM profile content") {
		t.Error("DM session should contain user profile content")
	}
}

func TestKnowledgeFactsNotInjectedIntoPrompt(t *testing.T) {
	fake := memorytest.New()
	ctx := context.Background()

	fake.AddFact("u1", "a1", memory.Fact{
		ID:      "knowledge-1",
		Subject: memory.FactSubjectWorld,
		Content: "PostgreSQL bundles target Ubuntu LTS runtimes.",
		Status:  memory.FactStatusActive,
	})
	facts, err := fake.ListActiveFacts(ctx, "u1", "a1", memory.FactSubjectWorld)
	if err != nil || len(facts) != 1 {
		t.Fatalf("test setup failed, facts=%#v err=%v", facts, err)
	}

	p := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Memory:       fake,
		UserID:       "u1",
		AgentID:      "a1",
	})

	if strings.Contains(p, "## Knowledge") {
		t.Fatalf("did not expect Knowledge section; knowledge should be retrieved with memory.search:\n%s", p)
	}
	if strings.Contains(p, "PostgreSQL bundles target Ubuntu LTS runtimes.") {
		t.Fatalf("did not expect world fact content to be injected into prompt:\n%s", p)
	}
}
