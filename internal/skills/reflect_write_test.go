package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateReflectOwnedUserAgentSkillRecordsVersionAndChangelog(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)

	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-created",
		Description:     "created by reflect",
		MainFileContent: "# Reflect Created\n",
		Metadata:        json.RawMessage(`{"created-at":"2026-07-06T00:00:00Z"}`),
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}

	if created.Version != 1 {
		t.Fatalf("created version = %d, want 1", created.Version)
	}
	if !IsReflectOwned(created) {
		t.Fatalf("created skill is not reflect-owned: %#v", created)
	}
	content, err := store.LoadFile(ctx, created.ID, MainFile)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if content != "# Reflect Created\n" {
		t.Fatalf("main file = %q", content)
	}

	var action string
	var versionAfter int64
	if err := db.QueryRow(ctx, `
		SELECT action, version_after
		FROM skill_changelog
		WHERE skill_id = $1
	`, created.ID).Scan(&action, &versionAfter); err != nil {
		t.Fatalf("read skill changelog: %v", err)
	}
	if action != "create" || versionAfter != 1 {
		t.Fatalf("changelog = action:%s version_after:%d", action, versionAfter)
	}

	var redundantColumnCount int
	if err := db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_name = 'skill_changelog'
		  AND column_name IN ('source', 'before_snapshot', 'after_snapshot')
	`).Scan(&redundantColumnCount); err != nil {
		t.Fatalf("inspect skill_changelog columns: %v", err)
	}
	if redundantColumnCount != 0 {
		t.Fatalf("skill_changelog has %d redundant source/snapshot columns", redundantColumnCount)
	}
}

func TestCreateReflectOwnedUserAgentSkillInitializesUsage(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)

	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-usage-create",
		Description:     "created by reflect",
		MainFileContent: "# Reflect Usage Create\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}

	var useCount int64
	var lastUsedAt time.Time
	if err := db.QueryRow(ctx, `
		SELECT use_count, last_used_at
		FROM skill_usage
		WHERE skill_id = $1 AND user_id = $2 AND agent_id = $3
	`, created.ID, userID, agentID).Scan(&useCount, &lastUsedAt); err != nil {
		t.Fatalf("read created skill usage: %v", err)
	}
	if useCount != 1 {
		t.Fatalf("created skill use_count = %d, want 1", useCount)
	}
	if lastUsedAt.IsZero() {
		t.Fatal("created skill last_used_at is zero")
	}
}

func TestPatchReflectOwnedUserAgentSkillUsesOptimisticVersionAndChangelog(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-patched",
		Description:     "before",
		MainFileContent: "# Before\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}

	afterDescription := "after"
	afterContent := "# After\n"
	patched, err := store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{
		ID:              created.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: created.Version,
		Description:     &afterDescription,
		MainFileContent: &afterContent,
	})
	if err != nil {
		t.Fatalf("PatchReflectOwnedUserAgentSkill: %v", err)
	}
	if patched.Version != created.Version+1 {
		t.Fatalf("patched version = %d, want %d", patched.Version, created.Version+1)
	}
	if patched.Description != afterDescription {
		t.Fatalf("patched description = %q, want %q", patched.Description, afterDescription)
	}
	content, err := store.LoadFile(ctx, created.ID, MainFile)
	if err != nil {
		t.Fatalf("LoadFile after patch: %v", err)
	}
	if content != afterContent {
		t.Fatalf("main file after patch = %q", content)
	}

	var patchCount int
	if err := db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM skill_changelog
		WHERE skill_id = $1 AND action = 'patch' AND version_before = 1 AND version_after = 2
	`, created.ID).Scan(&patchCount); err != nil {
		t.Fatalf("count patch changelog: %v", err)
	}
	if patchCount != 1 {
		t.Fatalf("patch changelog count = %d, want 1", patchCount)
	}

	logs, err := store.ListSkillChangelogBySkill(ctx, created.ID, 10)
	if err != nil {
		t.Fatalf("ListSkillChangelogBySkill: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("got %d changelog entries, want 2: %#v", len(logs), logs)
	}
	if logs[0].Action != "patch" || logs[0].VersionAfter != 2 {
		t.Fatalf("latest changelog = %#v, want patch v2", logs[0])
	}
	if logs[1].Action != "create" || logs[1].VersionAfter != 1 {
		t.Fatalf("oldest changelog = %#v, want create v1", logs[1])
	}

	_, err = store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{
		ID:              created.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: created.Version,
		MainFileContent: &afterContent,
	})
	if !errors.Is(err, ErrSkillVersionConflict) {
		t.Fatalf("stale patch error = %v, want ErrSkillVersionConflict", err)
	}
}

func TestPatchReflectOwnedUserAgentSkillRefreshesUsageWithoutIncrementingCount(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-usage-patch",
		Description:     "before",
		MainFileContent: "# Before\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}

	oldLastUsed := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := db.Exec(ctx, `
		UPDATE skill_usage
		SET last_used_at = $1, use_count = 7
		WHERE skill_id = $2
	`, oldLastUsed, created.ID); err != nil {
		t.Fatalf("seed skill usage: %v", err)
	}

	desc := "after"
	if _, err := store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{
		ID:              created.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: created.Version,
		Description:     &desc,
	}); err != nil {
		t.Fatalf("PatchReflectOwnedUserAgentSkill: %v", err)
	}

	var useCount int64
	var lastUsedAt time.Time
	if err := db.QueryRow(ctx, `
		SELECT use_count, last_used_at
		FROM skill_usage
		WHERE skill_id = $1
	`, created.ID).Scan(&useCount, &lastUsedAt); err != nil {
		t.Fatalf("read patched skill usage: %v", err)
	}
	if useCount != 7 {
		t.Fatalf("patched skill use_count = %d, want 7", useCount)
	}
	if !lastUsedAt.After(oldLastUsed) {
		t.Fatalf("patched skill last_used_at = %v, want after %v", lastUsedAt, oldLastUsed)
	}
}

func TestDeprecateReflectOwnedUserAgentSkillDeletesUsageAndRecordsChangelog(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-usage-deprecate",
		Description:     "before",
		MainFileContent: "# Before\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}

	deprecated, err := store.DeprecateReflectOwnedUserAgentSkill(ctx, ReflectSkillDeprecate{
		ID:              created.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: created.Version,
	})
	if err != nil {
		t.Fatalf("DeprecateReflectOwnedUserAgentSkill: %v", err)
	}
	if deprecated.Status != SkillStatusDeprecated {
		t.Fatalf("deprecated status = %q, want %q", deprecated.Status, SkillStatusDeprecated)
	}
	if deprecated.Version != created.Version+1 {
		t.Fatalf("deprecated version = %d, want %d", deprecated.Version, created.Version+1)
	}

	var usageCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*)
		FROM skill_usage
		WHERE skill_id = $1
	`, created.ID).Scan(&usageCount); err != nil {
		t.Fatalf("count skill_usage: %v", err)
	}
	if usageCount != 0 {
		t.Fatalf("deprecated skill usage rows = %d, want 0", usageCount)
	}

	var changelogCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*)
		FROM skill_changelog
		WHERE skill_id = $1
		  AND action = 'deprecate'
		  AND version_before = $2
		  AND version_after = $3
	`, created.ID, created.Version, deprecated.Version).Scan(&changelogCount); err != nil {
		t.Fatalf("count deprecate changelog: %v", err)
	}
	if changelogCount != 1 {
		t.Fatalf("deprecate changelog count = %d, want 1", changelogCount)
	}
}

func TestDeprecateReflectOwnedUserAgentSkillRejectsManualSkill(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)

	id, err := store.Create(ctx, Skill{
		Scope:       "user_agent",
		UserID:      userID,
		AgentID:     agentID,
		Name:        "manual-deprecate-skill",
		Description: "manual",
		Status:      "active",
	}, map[string]string{MainFile: "# Manual\n"})
	if err != nil {
		t.Fatalf("Create manual skill: %v", err)
	}

	_, err = store.DeprecateReflectOwnedUserAgentSkill(ctx, ReflectSkillDeprecate{
		ID:              id,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: 1,
	})
	if !errors.Is(err, ErrSkillNotReflectOwned) {
		t.Fatalf("manual deprecate error = %v, want ErrSkillNotReflectOwned", err)
	}
}

func TestSkillChangelogAcceptsLifecycleActionsForReflectCurator(t *testing.T) {
	_, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)

	_, err := db.Exec(ctx, `
		INSERT INTO skill (id, scope, user_id, agent_id, name, description, status, metadata)
		VALUES ('reflect-lifecycle-guard', 'user_agent', $1, $2, 'reflect-lifecycle-guard', 'guard', 'active', '{"created_by":"reflect"}')
	`, userID, agentID)
	if err != nil {
		t.Fatalf("insert skill: %v", err)
	}

	_, err = db.Exec(ctx, `
		INSERT INTO skill_changelog (skill_id, user_id, agent_id, scope, action, version_after)
		VALUES ('reflect-lifecycle-guard', $1, $2, 'user_agent', 'deprecate', 2)
	`, userID, agentID)
	if err != nil {
		t.Fatalf("expected deprecate changelog action to be allowed for #535: %v", err)
	}
}

func TestPatchReflectOwnedUserAgentSkillRejectsManualSkill(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)

	id, err := store.Create(ctx, Skill{
		Scope:       "user_agent",
		UserID:      userID,
		AgentID:     agentID,
		Name:        "manual-skill",
		Description: "manual",
		Status:      "active",
	}, map[string]string{MainFile: "# Manual\n"})
	if err != nil {
		t.Fatalf("Create manual skill: %v", err)
	}

	content := "# Should Not Write\n"
	_, err = store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{
		ID:              id,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: 1,
		MainFileContent: &content,
	})
	if !errors.Is(err, ErrSkillNotReflectOwned) {
		t.Fatalf("manual patch error = %v, want ErrSkillNotReflectOwned", err)
	}
}

func TestDiskSyncReflectOwnedSkillWritesMainFile(t *testing.T) {
	raw, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	baseDir := t.TempDir()
	store := NewDiskSyncStore(raw, func(scope, agentID string, userID string) string {
		if scope == "user_agent" {
			return baseDir
		}
		return ""
	})

	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-disk",
		Description:     "created by reflect",
		MainFileContent: "# Before\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}
	diskPath := filepath.Join(baseDir, "reflect-disk", MainFile)
	content, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("read disk SKILL.md after create: %v", err)
	}
	if string(content) != "# Before\n" {
		t.Fatalf("disk SKILL.md after create = %q", content)
	}

	afterContent := "# After\n"
	if _, err := store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{
		ID:              created.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: created.Version,
		MainFileContent: &afterContent,
	}); err != nil {
		t.Fatalf("PatchReflectOwnedUserAgentSkill: %v", err)
	}
	content, err = os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("read disk SKILL.md after patch: %v", err)
	}
	if string(content) != afterContent {
		t.Fatalf("disk SKILL.md after patch = %q, want %q", content, afterContent)
	}
}

func TestDiskSyncReflectPatchDoesNotWriteDiskOnVersionConflict(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	metadata, err := MarkReflectOwnedMetadata(nil)
	if err != nil {
		t.Fatalf("MarkReflectOwnedMetadata: %v", err)
	}
	skill := Skill{
		ID:       "reflect-conflict",
		Scope:    "user_agent",
		UserID:   "user-1",
		AgentID:  "agent-1",
		Name:     "reflect-conflict",
		Status:   "active",
		Version:  1,
		Metadata: metadata,
	}
	inner := &conflictingReflectPatchStore{
		skill:    skill,
		patchErr: ErrSkillVersionConflict,
	}
	store := NewDiskSyncStore(inner, func(scope, agentID string, userID string) string {
		if scope == "user_agent" {
			return baseDir
		}
		return ""
	})
	diskPath := filepath.Join(baseDir, skill.Name, MainFile)
	if err := writeFile(diskPath, "# Before\n"); err != nil {
		t.Fatalf("seed disk SKILL.md: %v", err)
	}

	staleContent := "# Stale Patch\n"
	_, err = store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{
		ID:              skill.ID,
		UserID:          skill.UserID,
		AgentID:         skill.AgentID,
		ExpectedVersion: skill.Version,
		MainFileContent: &staleContent,
	})
	if !errors.Is(err, ErrSkillVersionConflict) {
		t.Fatalf("PatchReflectOwnedUserAgentSkill error = %v, want ErrSkillVersionConflict", err)
	}
	if !inner.patchCalled {
		t.Fatal("expected inner store patch to be attempted")
	}
	content, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("read disk SKILL.md after conflict: %v", err)
	}
	if got := string(content); got != "# Before\n" {
		t.Fatalf("disk SKILL.md was modified on version conflict: %q", got)
	}
}

type conflictingReflectPatchStore struct {
	Store
	skill       Skill
	patchErr    error
	patchCalled bool
}

func (s *conflictingReflectPatchStore) ListAll(context.Context) ([]Skill, error) {
	return []Skill{s.skill}, nil
}

func (s *conflictingReflectPatchStore) PatchReflectOwnedUserAgentSkill(context.Context, ReflectSkillPatch) (Skill, error) {
	s.patchCalled = true
	if s.patchErr != nil {
		return Skill{}, s.patchErr
	}
	return s.skill, nil
}
