package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

func TestTouchReflectSkillRuntimeUseDoesNotRecreateMissingRow(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "runtime-update-only",
		Description: "runtime update only", MainFileContent: "# Runtime Update Only\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM skill_usage WHERE skill_id = $1`, created.ID); err != nil {
		t.Fatalf("delete skill usage: %v", err)
	}

	if err := store.TouchReflectSkillRuntimeUse(ctx, created.ID, userID, agentID); err != nil {
		t.Fatalf("TouchReflectSkillRuntimeUse: %v", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM skill_usage WHERE skill_id = $1`, created.ID).Scan(&count); err != nil {
		t.Fatalf("count skill usage: %v", err)
	}
	if count != 0 {
		t.Fatalf("skill usage rows = %d, runtime touch must be UPDATE-only", count)
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

func TestCreateReflectOwnedSkillExactRetryReturnsExisting(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	request := ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-create-retry",
		Description:     "  created by reflect  ",
		MainFileContent: "# Create Retry\n",
		Metadata:        json.RawMessage(`{"source":"review","created-at":"2026-07-13T00:00:00Z"}`),
	}

	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, request)
	if err != nil {
		t.Fatalf("first CreateReflectOwnedUserAgentSkill: %v", err)
	}
	var firstLastUsedAt time.Time
	if err := db.QueryRow(ctx, `SELECT last_used_at FROM skill_usage WHERE skill_id = $1`, created.ID).Scan(&firstLastUsedAt); err != nil {
		t.Fatalf("read initial usage timestamp: %v", err)
	}

	// Different JSON formatting represents the same requested metadata state.
	request.Metadata = json.RawMessage("{\n  \"created-at\": \"2026-07-13T00:00:00Z\",\n  \"source\": \"review\"\n}")
	retried, err := store.CreateReflectOwnedUserAgentSkill(ctx, request)
	if err != nil {
		t.Fatalf("retry CreateReflectOwnedUserAgentSkill: %v", err)
	}
	if retried.ID != created.ID || retried.Version != created.Version {
		t.Fatalf("retry returned id/version = %s/%d, want %s/%d", retried.ID, retried.Version, created.ID, created.Version)
	}
	if retried.Description != "created by reflect" {
		t.Fatalf("normalized description = %q, want %q", retried.Description, "created by reflect")
	}

	var changelogCount int
	var retriedLastUsedAt time.Time
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM skill_changelog WHERE skill_id = $1`, created.ID).Scan(&changelogCount); err != nil {
		t.Fatalf("count retry changelog: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT last_used_at FROM skill_usage WHERE skill_id = $1`, created.ID).Scan(&retriedLastUsedAt); err != nil {
		t.Fatalf("read retried usage timestamp: %v", err)
	}
	if changelogCount != 1 {
		t.Fatalf("retry changelog count = %d, want 1", changelogCount)
	}
	if !retriedLastUsedAt.Equal(firstLastUsedAt) {
		t.Fatalf("retry refreshed usage timestamp from %v to %v", firstLastUsedAt, retriedLastUsedAt)
	}
}

func TestCreateReflectOwnedSkillLargeIntegerMetadataConflict(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	request := ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-large-metadata",
		Description:     "large integer metadata",
		MainFileContent: "# Large Metadata\n",
		Metadata:        json.RawMessage(`{"sequence":9007199254740992}`),
	}
	if _, err := store.CreateReflectOwnedUserAgentSkill(ctx, request); err != nil {
		t.Fatalf("first CreateReflectOwnedUserAgentSkill: %v", err)
	}

	request.Metadata = json.RawMessage(`{"sequence":9007199254740993}`)
	_, err := store.CreateReflectOwnedUserAgentSkill(ctx, request)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "idx_skill_owner_name" {
		t.Fatalf("different large-integer metadata error = %v, want owner/name conflict", err)
	}
}

func TestCreateReflectOwnedSkillSameNameDifferentStateConflicts(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	request := ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-create-conflict",
		Description:     "original description",
		MainFileContent: "# Original\n",
		Metadata:        json.RawMessage(`{"source":"review"}`),
	}
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, request)
	if err != nil {
		t.Fatalf("first CreateReflectOwnedUserAgentSkill: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ReflectSkillCreate)
	}{
		{name: "description", mutate: func(in *ReflectSkillCreate) { in.Description = "different description" }},
		{name: "metadata", mutate: func(in *ReflectSkillCreate) { in.Metadata = json.RawMessage(`{"source":"other"}`) }},
		{name: "main file", mutate: func(in *ReflectSkillCreate) { in.MainFileContent = "# Different\n" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conflicting := request
			tt.mutate(&conflicting)
			if _, err := store.CreateReflectOwnedUserAgentSkill(ctx, conflicting); err == nil {
				t.Fatal("same-name create with different desired state succeeded")
			}
		})
	}

	content, err := store.LoadFile(ctx, created.ID, MainFile)
	if err != nil {
		t.Fatalf("LoadFile after conflicts: %v", err)
	}
	if content != request.MainFileContent {
		t.Fatalf("conflicting create changed SKILL.md to %q", content)
	}

	stateTests := []struct {
		name   string
		slug   string
		update string
	}{
		{name: "manual ownership", slug: "manual", update: `UPDATE skill SET metadata = '{}' WHERE id = $1`},
		{name: "model invocation disabled", slug: "disabled", update: `UPDATE skill SET disable_model_invocation = true WHERE id = $1`},
	}
	for _, tt := range stateTests {
		t.Run(tt.name, func(t *testing.T) {
			stateRequest := request
			stateRequest.Name += "-" + tt.slug
			stateCreated, err := store.CreateReflectOwnedUserAgentSkill(ctx, stateRequest)
			if err != nil {
				t.Fatalf("create state fixture: %v", err)
			}
			if _, err := db.Exec(ctx, tt.update, stateCreated.ID); err != nil {
				t.Fatalf("mutate state fixture: %v", err)
			}
			if _, err := store.CreateReflectOwnedUserAgentSkill(ctx, stateRequest); err == nil {
				t.Fatal("same-name retry accepted an ineligible existing skill")
			}
		})
	}

	t.Run("legacy deprecated name still conflicts", func(t *testing.T) {
		stateRequest := request
		stateRequest.Name += "-deprecated"
		deprecated, err := store.CreateReflectOwnedUserAgentSkill(ctx, stateRequest)
		if err != nil {
			t.Fatalf("create deprecated fixture: %v", err)
		}
		if _, err := db.Exec(ctx, `UPDATE skill SET status = 'deprecated' WHERE id = $1`, deprecated.ID); err != nil {
			t.Fatalf("deprecate fixture: %v", err)
		}

		// New code physically deletes skills instead of creating deprecated rows.
		// A legacy deprecated row keeps its unique name until explicitly cleaned.
		if _, err := store.CreateReflectOwnedUserAgentSkill(ctx, stateRequest); err == nil {
			t.Fatal("same-name create succeeded while a legacy deprecated row exists")
		}
	})
}

func TestDeleteReflectOwnedUserAgentSkillRechecksUsageAndRemovesRows(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "reflect-delete-stale",
		Description: "stale reflect skill", MainFileContent: "# Stale\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}

	lastUsedAt := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Microsecond)
	if _, err := db.Exec(ctx, `UPDATE skill_usage SET last_used_at = $1 WHERE skill_id = $2`, lastUsedAt, created.ID); err != nil {
		t.Fatalf("seed stale usage: %v", err)
	}
	_, err = store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{
		ID: created.ID, UserID: userID, AgentID: agentID, ExpectedVersion: created.Version,
	})
	if err == nil {
		t.Fatal("delete without expected usage timestamp succeeded")
	}
	_, err = store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{
		ID: created.ID, UserID: userID, AgentID: agentID, ExpectedVersion: created.Version,
		ExpectedUsageLastUsedAt: lastUsedAt,
	})
	if !errors.Is(err, ErrSkillUsageChanged) {
		t.Fatalf("delete without later activity error = %v, want ErrSkillUsageChanged", err)
	}

	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id, last_active)
		VALUES ('00000000-0000-0000-0000-000000000123', 'reflect-delete-session', 'test', 'chat', $1, $2, $3)
	`, agentID, userID, lastUsedAt.Add(time.Hour)); err != nil {
		t.Fatalf("seed eligible activity: %v", err)
	}

	deleted, err := store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{
		ID: created.ID, UserID: userID, AgentID: agentID, ExpectedVersion: created.Version,
		ExpectedUsageLastUsedAt: lastUsedAt,
	})
	if err != nil {
		t.Fatalf("DeleteReflectOwnedUserAgentSkill: %v", err)
	}
	if deleted.ID != created.ID {
		t.Fatalf("deleted skill = %#v, want %s", deleted, created.ID)
	}

	for table, column := range map[string]string{
		"skill": "id", "skill_file": "skill_id", "skill_usage": "skill_id", "skill_changelog": "skill_id",
	} {
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE `+column+` = $1`, created.ID).Scan(&count); err != nil {
			t.Fatalf("count %s rows: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows after delete = %d, want 0", table, count)
		}
	}
}

func TestReflectCreateRetryOnlyRecognizesOwnerNameUniqueConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "owner name", err: &pgconn.PgError{Code: "23505", ConstraintName: "idx_skill_owner_name"}, want: true},
		{name: "primary key", err: &pgconn.PgError{Code: "23505", ConstraintName: "skill_pkey"}, want: false},
		{name: "other database error", err: &pgconn.PgError{Code: "23503", ConstraintName: "idx_skill_owner_name"}, want: false},
		{name: "plain error", err: errors.New("database unavailable"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSkillOwnerNameUniqueViolation(tt.err); got != tt.want {
				t.Fatalf("isSkillOwnerNameUniqueViolation() = %v, want %v", got, tt.want)
			}
		})
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

	retried, err := store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{
		ID:              created.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: created.Version,
		MainFileContent: &afterContent,
	})
	if err != nil {
		t.Fatalf("exact stale patch retry: %v", err)
	}
	if retried.Version != patched.Version {
		t.Fatalf("exact stale retry version = %d, want %d", retried.Version, patched.Version)
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

func TestPatchReflectOwnedSkillExactStaleRetryIsNoop(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-patch-retry",
		Description:     "before",
		MainFileContent: "# Before\n",
		Metadata:        json.RawMessage(`{"source":"before"}`),
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}

	description := "after"
	content := "# After\n"
	metadata := json.RawMessage(`{"source":"after","score":1}`)
	request := ReflectSkillPatch{
		ID:              created.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: created.Version,
		Description:     &description,
		MainFileContent: &content,
		Metadata:        metadata,
	}
	patched, err := store.PatchReflectOwnedUserAgentSkill(ctx, request)
	if err != nil {
		t.Fatalf("first PatchReflectOwnedUserAgentSkill: %v", err)
	}

	// A concurrent change to an unspecified field must not invalidate this retry.
	if _, err := db.Exec(ctx, `UPDATE skill SET status = 'draft' WHERE id = $1`, created.ID); err != nil {
		t.Fatalf("seed concurrent unspecified-field change: %v", err)
	}
	var firstLastUsedAt time.Time
	if err := db.QueryRow(ctx, `SELECT last_used_at FROM skill_usage WHERE skill_id = $1`, created.ID).Scan(&firstLastUsedAt); err != nil {
		t.Fatalf("read patched usage timestamp: %v", err)
	}
	request.Metadata = json.RawMessage(`{"score":1.0,"source":"after"}`)
	retried, err := store.PatchReflectOwnedUserAgentSkill(ctx, request)
	if err != nil {
		t.Fatalf("retry PatchReflectOwnedUserAgentSkill: %v", err)
	}
	if retried.Version != patched.Version || retried.Status != "draft" {
		t.Fatalf("retry returned version/status = %d/%q, want %d/draft", retried.Version, retried.Status, patched.Version)
	}

	var changelogCount int
	var retriedLastUsedAt time.Time
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM skill_changelog WHERE skill_id = $1`, created.ID).Scan(&changelogCount); err != nil {
		t.Fatalf("count patch retry changelog: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT last_used_at FROM skill_usage WHERE skill_id = $1`, created.ID).Scan(&retriedLastUsedAt); err != nil {
		t.Fatalf("read patch retry usage timestamp: %v", err)
	}
	if changelogCount != 2 {
		t.Fatalf("patch retry changelog count = %d, want create+patch only", changelogCount)
	}
	if !retriedLastUsedAt.Equal(firstLastUsedAt) {
		t.Fatalf("patch retry refreshed usage timestamp from %v to %v", firstLastUsedAt, retriedLastUsedAt)
	}
}

func TestPatchReflectOwnedSkillStaleDifferentStateConflicts(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-patch-conflict",
		Description:     "before",
		MainFileContent: "# Before\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}
	applied := "applied"
	if _, err := store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{
		ID: created.ID, UserID: userID, AgentID: agentID, ExpectedVersion: created.Version, Description: &applied,
	}); err != nil {
		t.Fatalf("advance skill version: %v", err)
	}

	different := "different"
	_, err = store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{
		ID: created.ID, UserID: userID, AgentID: agentID, ExpectedVersion: created.Version, Description: &different,
	})
	if !errors.Is(err, ErrSkillVersionConflict) {
		t.Fatalf("stale different-state patch error = %v, want ErrSkillVersionConflict", err)
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
