package skills

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
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

func TestSkillChangelogRejectsLifecycleActionsBeforeLifecycleIssue(t *testing.T) {
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
	if err == nil {
		t.Fatal("expected deprecate changelog action to be rejected before #535")
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
