package selfimprove

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/skills"
)

func TestExpireDrafts(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")

	// Create an old draft skill (created 60 days ago).
	oldCreated := time.Now().Add(-60 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := skills.Create("old-draft", "An old draft skill", "Old content", skillsDir); err != nil {
		t.Fatalf("create old-draft: %v", err)
	}
	// Rewrite with old created-at date.
	oldSkillFile := filepath.Join(skillsDir, "old-draft", "SKILL.md")
	oldContent := "---\ncreated-at: \"" + oldCreated + "\"\ndescription: An old draft skill\nname: old-draft\nstatus: draft\n---\nOld content\n"
	if err := os.WriteFile(oldSkillFile, []byte(oldContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a recent draft skill (created now).
	if err := skills.Create("new-draft", "A new draft skill", "New content", skillsDir); err != nil {
		t.Fatalf("create new-draft: %v", err)
	}

	log := slog.Default()

	// Expire drafts older than 30 days.
	ExpireDrafts(workspace, 30*24*time.Hour, log)

	// Old draft should be deprecated.
	data, err := os.ReadFile(oldSkillFile)
	if err != nil {
		t.Fatalf("read old skill: %v", err)
	}
	if !containsSubstring(string(data), "status: deprecated") {
		t.Errorf("old draft not deprecated: %s", data)
	}

	// New draft should still be draft.
	newSkillFile := filepath.Join(skillsDir, "new-draft", "SKILL.md")
	data, err = os.ReadFile(newSkillFile)
	if err != nil {
		t.Fatalf("read new skill: %v", err)
	}
	if !containsSubstring(string(data), "status: draft") {
		t.Errorf("new draft should still be draft: %s", data)
	}
}

func TestExpireDrafts_EmptyWorkspace(t *testing.T) {
	t.Parallel()
	// Should not panic on empty workspace.
	ExpireDrafts("", 30*24*time.Hour, slog.Default())
}

func TestExpireDrafts_NonexistentWorkspace(t *testing.T) {
	t.Parallel()
	// Should not panic on nonexistent workspace.
	ExpireDrafts("/nonexistent/path", 30*24*time.Hour, slog.Default())
}
