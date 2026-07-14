package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiskSyncReflectCreateDoesNotWriteRejectedConflict(t *testing.T) {
	raw, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	baseDir := t.TempDir()
	store := NewDiskSyncStore(raw, func(scope, agentID string, userID string) string {
		if scope == "user_agent" {
			return baseDir
		}
		return ""
	})
	original := ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-disk-create-conflict",
		Description:     "original",
		MainFileContent: "# Original\n",
	}
	if _, err := store.CreateReflectOwnedUserAgentSkill(ctx, original); err != nil {
		t.Fatalf("first CreateReflectOwnedUserAgentSkill: %v", err)
	}

	conflicting := original
	conflicting.Description = "different"
	conflicting.MainFileContent = "# Rejected\n"
	if _, err := store.CreateReflectOwnedUserAgentSkill(ctx, conflicting); err == nil {
		t.Fatal("conflicting reflect create succeeded")
	}

	diskPath := filepath.Join(baseDir, original.Name, MainFile)
	content, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("read disk SKILL.md after conflict: %v", err)
	}
	if got := string(content); got != original.MainFileContent {
		t.Fatalf("rejected create changed disk SKILL.md to %q", got)
	}
}

func TestDiskSyncReflectCreateRetriesMirrorAfterDiskFailure(t *testing.T) {
	raw, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	baseDir := t.TempDir()
	blockedBase := filepath.Join(baseDir, "blocked")
	if err := os.WriteFile(blockedBase, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed blocked mirror base: %v", err)
	}
	resolverCalls := 0
	store := NewDiskSyncStore(raw, func(scope, agentID string, userID string) string {
		resolverCalls++
		if resolverCalls == 1 {
			return blockedBase
		}
		return baseDir
	})
	request := ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-disk-create-retry",
		Description:     "retry disk mirror",
		MainFileContent: "# Mirror Retry\n",
	}

	if _, err := store.CreateReflectOwnedUserAgentSkill(ctx, request); err == nil {
		t.Fatal("first reflect create unexpectedly mirrored to blocked path")
	}
	var skillCount int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM skill WHERE name = $1`, request.Name).Scan(&skillCount); err != nil {
		t.Fatalf("count committed skill after mirror failure: %v", err)
	}
	if skillCount != 1 {
		t.Fatalf("skill rows after mirror failure = %d, want committed DB row", skillCount)
	}

	retried, err := store.CreateReflectOwnedUserAgentSkill(ctx, request)
	if err != nil {
		t.Fatalf("retry CreateReflectOwnedUserAgentSkill: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(baseDir, request.Name, MainFile))
	if err != nil {
		t.Fatalf("read retried disk mirror: %v", err)
	}
	if string(content) != request.MainFileContent {
		t.Fatalf("retried disk mirror = %q, want %q", content, request.MainFileContent)
	}
	if retried.Version != 1 {
		t.Fatalf("retried skill version = %d, want 1", retried.Version)
	}
}

func TestDiskSyncReflectPatchExactStaleRetryDoesNotRewriteDisk(t *testing.T) {
	raw, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	baseDir := t.TempDir()
	store := NewDiskSyncStore(raw, func(scope, agentID string, userID string) string {
		return baseDir
	})
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-disk-patch-retry",
		Description:     "disk patch retry",
		MainFileContent: "# Before\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}
	afterContent := "# After\n"
	request := ReflectSkillPatch{
		ID:              created.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: created.Version,
		MainFileContent: &afterContent,
	}
	if _, err := store.PatchReflectOwnedUserAgentSkill(ctx, request); err != nil {
		t.Fatalf("first PatchReflectOwnedUserAgentSkill: %v", err)
	}

	diskPath := filepath.Join(baseDir, created.Name, MainFile)
	oldTime := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(diskPath, oldTime, oldTime); err != nil {
		t.Fatalf("fix disk SKILL.md mtime: %v", err)
	}
	if _, err := store.PatchReflectOwnedUserAgentSkill(ctx, request); err != nil {
		t.Fatalf("exact stale retry PatchReflectOwnedUserAgentSkill: %v", err)
	}
	info, err := os.Stat(diskPath)
	if err != nil {
		t.Fatalf("stat disk SKILL.md after exact stale retry: %v", err)
	}
	if !info.ModTime().Equal(oldTime) {
		t.Fatalf("exact stale retry changed disk mtime from %v to %v", oldTime, info.ModTime())
	}
}
