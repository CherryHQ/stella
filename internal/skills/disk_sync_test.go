package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiskSyncReflectDeleteRemovesOnlyQuarantinedIdentity(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	sk := Skill{
		ID: "old-skill", Scope: "user_agent", UserID: "user-1", AgentID: "agent-1",
		Name: "reusable-name", Status: SkillStatusActive,
	}
	inner := &recreatingReflectDeleteStore{skill: sk, baseDir: baseDir}
	store := NewDiskSyncStore(inner, func(scope, agentID string, userID string) string { return baseDir })
	if err := writeFile(filepath.Join(baseDir, sk.Name, MainFile), "# Old\n"); err != nil {
		t.Fatalf("seed old mirror: %v", err)
	}

	if _, err := store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{ID: sk.ID}); err != nil {
		t.Fatalf("DeleteReflectOwnedUserAgentSkill: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(baseDir, sk.Name, MainFile))
	if err != nil {
		t.Fatalf("read recreated mirror: %v", err)
	}
	if string(content) != "# New\n" {
		t.Fatalf("recreated mirror content = %q, want new identity", content)
	}
	matches, err := filepath.Glob(filepath.Join(baseDir, ".stella-delete-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("delete tombstones = %v, err=%v", matches, err)
	}
}

func TestDiskSyncManagedDeleteRemovesOnlyQuarantinedIdentity(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	sk := Skill{ID: "old-manual", Scope: "user_agent", UserID: "user-1", AgentID: "agent-1", Name: "manual-reusable", Status: SkillStatusActive}
	inner := &recreatingReflectDeleteStore{skill: sk, baseDir: baseDir}
	store := NewDiskSyncStore(inner, func(scope, agentID string, userID string) string { return baseDir })
	if err := writeFile(filepath.Join(baseDir, sk.Name, MainFile), "# Old manual\n"); err != nil {
		t.Fatalf("seed old manual mirror: %v", err)
	}

	if err := store.Delete(ctx, sk.ID, ViewContext{UserID: sk.UserID, AgentID: sk.AgentID}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(baseDir, sk.Name, MainFile))
	if err != nil || string(content) != "# New\n" {
		t.Fatalf("recreated manual mirror = %q, err=%v", content, err)
	}
}

func TestDiskSyncReflectDeleteRestoresMirrorWhenStoreRejects(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	sk := Skill{ID: "kept-skill", Scope: "user_agent", UserID: "user-1", AgentID: "agent-1", Name: "kept-name", Status: SkillStatusActive}
	inner := &recreatingReflectDeleteStore{skill: sk, deleteErr: ErrSkillUsageChanged}
	store := NewDiskSyncStore(inner, func(scope, agentID string, userID string) string { return baseDir })
	path := filepath.Join(baseDir, sk.Name, MainFile)
	if err := writeFile(path, "# Kept\n"); err != nil {
		t.Fatalf("seed kept mirror: %v", err)
	}

	if _, err := store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{ID: sk.ID}); !errors.Is(err, ErrSkillUsageChanged) {
		t.Fatalf("delete error = %v, want ErrSkillUsageChanged", err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "# Kept\n" {
		t.Fatalf("restored mirror = %q, err=%v", content, err)
	}
}

type recreatingReflectDeleteStore struct {
	Store
	skill     Skill
	baseDir   string
	deleteErr error
}

func (s *recreatingReflectDeleteStore) ListAll(context.Context) ([]Skill, error) {
	return []Skill{s.skill}, nil
}

func (s *recreatingReflectDeleteStore) DeleteReflectOwnedUserAgentSkill(_ context.Context, _ ReflectSkillDelete) (Skill, error) {
	if s.deleteErr != nil {
		return Skill{}, s.deleteErr
	}
	if err := writeFile(filepath.Join(s.baseDir, s.skill.Name, MainFile), "# New\n"); err != nil {
		return Skill{}, err
	}
	return s.skill, nil
}

func (s *recreatingReflectDeleteStore) Delete(_ context.Context, _ string, _ ViewContext) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return writeFile(filepath.Join(s.baseDir, s.skill.Name, MainFile), "# New\n")
}

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
