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

func TestStoreInterfaceExposesReflectRestore(t *testing.T) {
	var store Store = (*PGStore)(nil)
	requireReflectRestoreStore(t, store)
}

func requireReflectRestoreStore(t *testing.T, store interface {
	RestoreReflectOwnedUserAgentSkill(context.Context, ReflectSkillRestore) (ReflectSkillRestoreResult, error)
},
) {
	t.Helper()
	if store == nil {
		t.Fatal("restore-capable store is nil")
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

	t.Run("deprecated name can be reused", func(t *testing.T) {
		stateRequest := request
		stateRequest.Name += "-deprecated"
		deprecated, err := store.CreateReflectOwnedUserAgentSkill(ctx, stateRequest)
		if err != nil {
			t.Fatalf("create deprecated fixture: %v", err)
		}
		if _, err := db.Exec(ctx, `UPDATE skill SET status = 'deprecated' WHERE id = $1`, deprecated.ID); err != nil {
			t.Fatalf("deprecate fixture: %v", err)
		}

		// Recoverable deprecated rows intentionally release their owner/name so a
		// newer active version can reuse the stable user-facing skill name.
		replacement, err := store.CreateReflectOwnedUserAgentSkill(ctx, stateRequest)
		if err != nil {
			t.Fatalf("create same-name replacement: %v", err)
		}
		if replacement.ID == deprecated.ID || replacement.Status != SkillStatusActive {
			t.Fatalf("replacement id/status = %s/%q, want a distinct active skill", replacement.ID, replacement.Status)
		}
	})
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

func TestRestoreReflectOwnedUserAgentSkillRestoresStatusChangelogAndUsage(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-usage-restore",
		Description:     "restore candidate",
		MainFileContent: "# Restore Candidate\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}
	deprecateMetadata := json.RawMessage(`{"curator":"usage","rule":"low_use","use_count":7,"last_used_at":"2026-06-01T00:00:00Z"}`)
	deprecated, err := store.DeprecateReflectOwnedUserAgentSkill(ctx, ReflectSkillDeprecate{
		ID:              created.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: created.Version,
		Metadata:        deprecateMetadata,
	})
	if err != nil {
		t.Fatalf("DeprecateReflectOwnedUserAgentSkill: %v", err)
	}

	result, err := store.RestoreReflectOwnedUserAgentSkill(ctx, ReflectSkillRestore{
		ID:         created.ID,
		UserID:     userID,
		AgentID:    agentID,
		RestoredBy: "admin@example.com",
		Reason:     "false positive",
	})
	if err != nil {
		t.Fatalf("RestoreReflectOwnedUserAgentSkill: %v", err)
	}
	if !result.Restored {
		t.Fatal("restore result Restored = false, want true")
	}
	if result.Skill.Status != SkillStatusActive || result.Skill.Version != deprecated.Version+1 {
		t.Fatalf("restored skill = %#v, want active version %d", result.Skill, deprecated.Version+1)
	}
	var useCount int64
	var lastUsedAt time.Time
	if err := db.QueryRow(ctx, `
		SELECT use_count, last_used_at
		FROM skill_usage
		WHERE skill_id = $1 AND user_id = $2 AND agent_id = $3
	`, created.ID, userID, agentID).Scan(&useCount, &lastUsedAt); err != nil {
		t.Fatalf("read restored skill usage: %v", err)
	}
	if useCount != 7 {
		t.Fatalf("restored use_count = %d, want old use_count 7", useCount)
	}
	if lastUsedAt.IsZero() {
		t.Fatal("restored last_used_at is zero")
	}
	logs, err := store.ListSkillChangelogBySkill(ctx, created.ID, 10)
	if err != nil {
		t.Fatalf("ListSkillChangelogBySkill: %v", err)
	}
	if logs[0].Action != "restore" || logs[0].VersionBefore != deprecated.Version || logs[0].VersionAfter != result.Skill.Version {
		t.Fatalf("latest changelog = %#v, want restore from deprecated version", logs[0])
	}
	var restoreMetadata map[string]any
	if err := json.Unmarshal(logs[0].Metadata, &restoreMetadata); err != nil {
		t.Fatalf("unmarshal restore metadata: %v", err)
	}
	if restoreMetadata["restored_by"] != "admin@example.com" || restoreMetadata["reason"] != "false positive" {
		t.Fatalf("restore metadata = %#v, want restored_by and reason", restoreMetadata)
	}
	if restoreMetadata["curator_rule"] != "low_use" || restoreMetadata["restored_use_count"].(float64) != 7 {
		t.Fatalf("restore metadata missing curator/use_count linkage: %#v", restoreMetadata)
	}

	second, err := store.RestoreReflectOwnedUserAgentSkill(ctx, ReflectSkillRestore{
		ID:         created.ID,
		UserID:     userID,
		AgentID:    agentID,
		RestoredBy: "admin@example.com",
	})
	if err != nil {
		t.Fatalf("RestoreReflectOwnedUserAgentSkill no-op: %v", err)
	}
	if second.Restored {
		t.Fatal("second restore Restored = true, want no-op")
	}
	if second.Skill.Version != result.Skill.Version {
		t.Fatalf("no-op restore bumped version to %d, want %d", second.Skill.Version, result.Skill.Version)
	}

	if _, err := store.DeprecateReflectOwnedUserAgentSkill(ctx, ReflectSkillDeprecate{
		ID:              created.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: second.Skill.Version,
	}); err != nil {
		t.Fatalf("DeprecateReflectOwnedUserAgentSkill manual: %v", err)
	}
	_, err = store.RestoreReflectOwnedUserAgentSkill(ctx, ReflectSkillRestore{
		ID:         created.ID,
		UserID:     userID,
		AgentID:    agentID,
		RestoredBy: "admin@example.com",
	})
	if !errors.Is(err, ErrSkillNotRestorable) {
		t.Fatalf("restore after latest manual deprecate err = %v, want ErrSkillNotRestorable", err)
	}
}

func TestRestoreReflectOwnedUserAgentSkillPreservesExplicitZeroUseCount(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-zero-use-restore",
		Description:     "zero-use restore candidate",
		MainFileContent: "# Zero-use Restore Candidate\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}
	deprecated, err := store.DeprecateReflectOwnedUserAgentSkill(ctx, ReflectSkillDeprecate{
		ID:              created.ID,
		UserID:          userID,
		AgentID:         agentID,
		ExpectedVersion: created.Version,
		Metadata:        json.RawMessage(`{"curator":"usage","rule":"unused","use_count":0,"last_used_at":"2026-06-01T00:00:00Z"}`),
	})
	if err != nil {
		t.Fatalf("DeprecateReflectOwnedUserAgentSkill: %v", err)
	}

	result, err := store.RestoreReflectOwnedUserAgentSkill(ctx, ReflectSkillRestore{
		ID:         created.ID,
		UserID:     userID,
		AgentID:    agentID,
		RestoredBy: "admin@example.com",
	})
	if err != nil {
		t.Fatalf("RestoreReflectOwnedUserAgentSkill: %v", err)
	}
	if !result.Restored || result.Skill.Version != deprecated.Version+1 {
		t.Fatalf("restore result = %#v, want restored version %d", result, deprecated.Version+1)
	}

	var useCount int64
	if err := db.QueryRow(ctx, `SELECT use_count FROM skill_usage WHERE skill_id = $1`, created.ID).Scan(&useCount); err != nil {
		t.Fatalf("read restored skill usage: %v", err)
	}
	if useCount != 0 {
		t.Fatalf("restored use_count = %d, want explicit historical value 0", useCount)
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
	_, err = db.Exec(ctx, `
		INSERT INTO skill_changelog (skill_id, user_id, agent_id, scope, action, version_after)
		VALUES ('reflect-lifecycle-guard', $1, $2, 'user_agent', 'restore', 3)
	`, userID, agentID)
	if err != nil {
		t.Fatalf("expected restore changelog action to be allowed for restore safety net: %v", err)
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
