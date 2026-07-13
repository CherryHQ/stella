package skills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiskSyncRejectedRemovedFileWritesLeaveMirrorUntouched(t *testing.T) {
	raw, db, ctx := newTestStore(t)
	userID, _ := seedFixtures(t, db)
	base := t.TempDir()
	store := NewDiskSyncStore(raw, func(scope, agentID, ownerID string) string {
		return base
	})

	id, err := store.Create(ctx, Skill{
		Scope: "user", UserID: userID, Name: "removed-mirror", Status: "active",
	}, map[string]string{MainFile: "original", "reference.md": "keep"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.DeprecateManagedSkill(ctx, ManagedSkillDeprecate{
		ID: id, Scope: "user", UserID: userID, DeprecatedBy: userID,
	}); err != nil {
		t.Fatalf("DeprecateManagedSkill: %v", err)
	}

	if err := store.UpsertFile(ctx, id, "reference.md", "changed"); !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("UpsertFile error = %v, want ErrSkillNotMutable", err)
	}
	if err := store.DeleteFile(ctx, id, "reference.md"); !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("DeleteFile error = %v, want ErrSkillNotMutable", err)
	}

	content, err := os.ReadFile(filepath.Join(base, "removed-mirror", "reference.md"))
	if err != nil {
		t.Fatalf("read retained mirror: %v", err)
	}
	if string(content) != "keep" {
		t.Fatalf("retained mirror = %q, want keep", content)
	}
}
