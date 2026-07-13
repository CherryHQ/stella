package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMarkManualOwnedMetadataPreservesMetadataAndNormalizesCreator(t *testing.T) {
	metadata, err := MarkManualOwnedMetadata(json.RawMessage(`{"source":"import","version":"v1","created_by":"reflect"}`))
	if err != nil {
		t.Fatalf("MarkManualOwnedMetadata: %v", err)
	}

	sk := Skill{Metadata: metadata}
	if got := CreatedBy(sk); got != ManualSkillCreatedBy {
		t.Fatalf("CreatedBy = %q, want %q", got, ManualSkillCreatedBy)
	}
	var fields map[string]string
	if err := json.Unmarshal(metadata, &fields); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if fields["source"] != "import" || fields["version"] != "v1" {
		t.Fatalf("metadata fields were not retained: %#v", fields)
	}

	// JSON permits insignificant whitespace around null, which must still be
	// normalized to a writable metadata object.
	metadata, err = MarkManualOwnedMetadata(json.RawMessage(" null "))
	if err != nil {
		t.Fatalf("MarkManualOwnedMetadata null: %v", err)
	}
	if got := CreatedBy(Skill{Metadata: metadata}); got != ManualSkillCreatedBy {
		t.Fatalf("null metadata created_by = %q, want manual", got)
	}
}

func TestManagedSkillGenericCreateNormalizesManualOwner(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)

	id, err := store.Create(ctx, Skill{
		Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "manual-normalized", Description: "d",
		Metadata: json.RawMessage(`{"source":"import","created_by":"reflect"}`),
	}, map[string]string{MainFile: "body"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	row, err := store.q.GetSkillByID(ctx, id)
	if err != nil {
		t.Fatalf("GetSkillByID: %v", err)
	}
	if got := CreatedBy(mapRow(row)); got != ManualSkillCreatedBy {
		t.Fatalf("created_by = %q, want manual", got)
	}
	if _, err := store.q.GetSkillUsageForUpdate(ctx, storeSkillUsageParams(id, userID, agentID)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("manual generic create usage = %v, want no row", err)
	}
}

func TestManagedSkillMutationScopesAndSystemRejection(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	description := "updated"

	for _, sk := range []Skill{
		{Scope: "user", UserID: userID, Name: "scope-user", Description: "d"},
		{Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "scope-user-agent", Description: "d"},
		{Scope: "system_agent", AgentID: agentID, Name: "scope-system-agent", Description: "d"},
	} {
		t.Run(sk.Scope, func(t *testing.T) {
			id, err := store.Create(ctx, sk, map[string]string{MainFile: "body"})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			updated, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{
				ID: id, UserID: userID, AgentID: agentID, Scope: sk.Scope,
				Patch: UpdatePatch{Description: &description},
			})
			if err != nil {
				t.Fatalf("UpdateManagedSkill: %v", err)
			}
			if updated.Description != description || updated.Version != 2 {
				t.Fatalf("updated skill = %#v", updated)
			}
		})
	}

	id, err := store.Create(ctx, Skill{Scope: "system", Name: "system", Description: "d"}, map[string]string{MainFile: "body"})
	if err != nil {
		t.Fatalf("Create system: %v", err)
	}
	_, err = store.UpdateManagedSkill(ctx, ManagedSkillUpdate{ID: id, Scope: "system", Patch: UpdatePatch{Description: &description}})
	if !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("system mutation error = %v, want ErrSkillNotMutable", err)
	}
	_, err = store.DeprecateManagedSkill(ctx, ManagedSkillDeprecate{ID: id, Scope: "system", DeprecatedBy: userID})
	if !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("system deprecate error = %v, want ErrSkillNotMutable", err)
	}
	_, err = store.RestoreManagedSkill(ctx, ManagedSkillRestore{ID: id, Scope: "system", RestoredBy: userID})
	if !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("system restore error = %v, want ErrSkillNotMutable", err)
	}
}

func TestManagedSkillDeprecateRestoreRetainsFilesChangelogAndUsage(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "retained", Description: "d", MainFileContent: "original",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}
	if err := store.UpsertFile(ctx, created.ID, "references/keep.md", "keep"); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	lastUsedAt := time.Date(2026, time.March, 8, 6, 30, 0, 0, time.UTC)
	if _, err := db.Exec(ctx, "UPDATE skill_usage SET use_count = 7, last_used_at = $1 WHERE skill_id = $2", lastUsedAt, created.ID); err != nil {
		t.Fatalf("seed reflect usage: %v", err)
	}

	deprecated, err := store.DeprecateManagedSkill(ctx, ManagedSkillDeprecate{
		ID: created.ID, UserID: userID, AgentID: agentID, Scope: "user_agent", DeprecatedBy: userID,
	})
	if err != nil {
		t.Fatalf("DeprecateManagedSkill: %v", err)
	}
	if deprecated.Status != "deprecated" || deprecated.Version != created.Version+1 {
		t.Fatalf("deprecated skill = %#v", deprecated)
	}
	if _, err := store.q.GetSkillUsageForUpdate(ctx, storeSkillUsageParams(created.ID, userID, agentID)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("usage after deprecate = %v, want no row", err)
	}

	logs, err := store.ListSkillChangelogBySkill(ctx, created.ID, 10)
	if err != nil {
		t.Fatalf("ListSkillChangelogBySkill: %v", err)
	}
	if len(logs) == 0 || logs[0].Action != "deprecate" {
		t.Fatalf("changelog = %#v, want deprecate entry", logs)
	}
	var deprecateMetadata map[string]any
	if err := json.Unmarshal(logs[0].Metadata, &deprecateMetadata); err != nil {
		t.Fatalf("decode deprecate metadata: %v", err)
	}
	if deprecateMetadata["deprecated_by"] != "manual" || deprecateMetadata["use_count"] == nil {
		t.Fatalf("deprecate metadata = %#v", deprecateMetadata)
	}

	restored, err := store.RestoreManagedSkill(ctx, ManagedSkillRestore{
		ID: created.ID, UserID: userID, AgentID: agentID, Scope: "user_agent", RestoredBy: userID,
		Now: deprecated.UpdatedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("RestoreManagedSkill: %v", err)
	}
	if !restored.Restored || restored.Skill.Status != "active" || !IsReflectOwned(restored.Skill) {
		t.Fatalf("restore result = %#v", restored)
	}
	files, err := store.ListFilesWithContent(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListFilesWithContent: %v", err)
	}
	if files[MainFile] != "original" || files["references/keep.md"] != "keep" {
		t.Fatalf("retained files = %#v", files)
	}
	usage, err := store.q.GetSkillUsageForUpdate(ctx, storeSkillUsageParams(created.ID, userID, agentID))
	if err != nil || usage.UseCount != 7 || !usage.LastUsedAt.Equal(lastUsedAt) {
		t.Fatalf("restored usage = %#v, %v", usage, err)
	}

	idempotent, err := store.RestoreManagedSkill(ctx, ManagedSkillRestore{
		ID: created.ID, UserID: userID, AgentID: agentID, Scope: "user_agent", RestoredBy: userID,
	})
	if err != nil || idempotent.Restored || idempotent.Skill.ID != created.ID {
		t.Fatalf("idempotent restore = %#v, %v", idempotent, err)
	}
}

func TestManagedSkillRestoreConflictExpiryAndDSTBoundary(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	old := mustCreateManagedSkill(t, store, ctx, Skill{Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "shared", Description: "d"})
	deprecated, err := store.DeprecateManagedSkill(ctx, ManagedSkillDeprecate{ID: old.ID, UserID: userID, AgentID: agentID, Scope: old.Scope, DeprecatedBy: userID})
	if err != nil {
		t.Fatalf("DeprecateManagedSkill: %v", err)
	}
	_ = mustCreateManagedSkill(t, store, ctx, Skill{Scope: old.Scope, UserID: userID, AgentID: agentID, Name: old.Name, Description: "replacement"})
	_, err = store.RestoreManagedSkill(ctx, ManagedSkillRestore{ID: old.ID, UserID: userID, AgentID: agentID, Scope: old.Scope, RestoredBy: userID, Now: deprecated.UpdatedAt.Add(time.Hour)})
	if !errors.Is(err, ErrSkillNameConflict) {
		t.Fatalf("restore conflict error = %v, want ErrSkillNameConflict", err)
	}

	expired := mustCreateManagedSkill(t, store, ctx, Skill{Scope: "user", UserID: userID, Name: "expiry", Description: "d"})
	_, err = store.DeprecateManagedSkill(ctx, ManagedSkillDeprecate{ID: expired.ID, UserID: userID, Scope: expired.Scope, DeprecatedBy: userID})
	if err != nil {
		t.Fatalf("DeprecateManagedSkill expiry: %v", err)
	}
	// The 90-day window is elapsed time, not a local calendar calculation around DST.
	dstLocation, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	// The interval crosses the 2026 US spring DST transition. Restore uses the
	// UTC elapsed-time deadline instead of a local calendar date.
	dstDeprecation := time.Date(2026, time.March, 8, 1, 30, 0, 0, dstLocation).UTC()
	if _, err := db.Exec(ctx, "UPDATE skill_changelog SET created_at = $1 WHERE skill_id = $2 AND action = 'deprecate'", dstDeprecation, expired.ID); err != nil {
		t.Fatalf("set DST deprecation time: %v", err)
	}
	deadline := dstDeprecation.Add(2160 * time.Hour)
	_, err = store.RestoreManagedSkill(ctx, ManagedSkillRestore{ID: expired.ID, UserID: userID, Scope: expired.Scope, RestoredBy: userID, Now: deadline})
	if !errors.Is(err, ErrSkillRestoreExpired) {
		t.Fatalf("restore at deadline error = %v, want ErrSkillRestoreExpired", err)
	}
}

func TestManagedSkillRestoreRejectsDraftInsteadOfTreatingItAsIdempotent(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, _ := seedFixtures(t, db)
	draft := mustCreateManagedSkill(t, store, ctx, Skill{
		Scope: "user", UserID: userID, Name: "draft", Description: "d", Status: "draft",
	})

	_, err := store.RestoreManagedSkill(ctx, ManagedSkillRestore{
		ID: draft.ID, UserID: userID, Scope: "user", RestoredBy: userID,
	})
	if !errors.Is(err, ErrSkillNotRestorable) {
		t.Fatalf("restore draft error = %v, want ErrSkillNotRestorable", err)
	}
}

func TestManagedSkillListIncludesDraftAndPagesRecoverableRemovals(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	draft := mustCreateManagedSkill(t, store, ctx, Skill{
		Scope: "user", UserID: userID, Name: "draft-visible", Description: "d", Status: "draft",
	})
	active := mustCreateManagedSkill(t, store, ctx, Skill{
		Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "active-visible", Description: "d",
	})
	if _, err := store.DeprecateManagedSkill(ctx, ManagedSkillDeprecate{
		ID: active.ID, UserID: userID, AgentID: agentID, Scope: "user_agent", DeprecatedBy: userID,
	}); err != nil {
		t.Fatalf("DeprecateManagedSkill: %v", err)
	}
	reflect, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "curator-removed", Description: "d", MainFileContent: "body",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}
	if _, err := store.DeprecateReflectOwnedUserAgentSkill(ctx, ReflectSkillDeprecate{
		ID: reflect.ID, UserID: userID, AgentID: agentID, ExpectedVersion: reflect.Version,
		Metadata: json.RawMessage(`{"curator":"usage","use_count":1}`),
	}); err != nil {
		t.Fatalf("DeprecateReflectOwnedUserAgentSkill: %v", err)
	}

	now := time.Now().UTC()
	activePage, err := store.ListManagedSkills(ctx, ManagedSkillListQuery{
		UserID: userID, AgentID: agentID, State: ManagedSkillStateActive, Limit: 10, Now: now,
	})
	if err != nil {
		t.Fatalf("ListManagedSkills active: %v", err)
	}
	if activePage.Total != 1 || len(activePage.Items) != 1 || activePage.Items[0].Skill.ID != draft.ID {
		t.Fatalf("active managed page = %#v, want draft only", activePage)
	}

	removedPage, err := store.ListManagedSkills(ctx, ManagedSkillListQuery{
		UserID: userID, AgentID: agentID, State: ManagedSkillStateRemoved, Limit: 1, Now: now,
	})
	if err != nil {
		t.Fatalf("ListManagedSkills removed: %v", err)
	}
	if removedPage.Total != 2 || len(removedPage.Items) != 1 || !removedPage.HasMore || !removedPage.Items[0].IsRestorable {
		t.Fatalf("removed managed page = %#v", removedPage)
	}
	if removedPage.Items[0].RestoreDeadline == nil || !removedPage.Items[0].RestoreDeadline.Equal(removedPage.Items[0].DeprecatedAt.Add(2160*time.Hour)) {
		t.Fatalf("removed restore window = %#v", removedPage.Items[0])
	}

	if _, err := db.Exec(ctx, "UPDATE skill_changelog SET created_at = $1 WHERE skill_id = $2 AND action = 'deprecate'", now.Add(-2160*time.Hour), active.ID); err != nil {
		t.Fatalf("expire manual deprecation: %v", err)
	}
	expiredPage, err := store.ListManagedSkills(ctx, ManagedSkillListQuery{
		UserID: userID, AgentID: agentID, State: ManagedSkillStateRemoved, Limit: 10, Now: now,
	})
	if err != nil {
		t.Fatalf("ListManagedSkills expired: %v", err)
	}
	if expiredPage.Total != 1 || len(expiredPage.Items) != 1 || expiredPage.Items[0].Skill.ID != reflect.ID {
		t.Fatalf("expired removed page = %#v, want curator row only", expiredPage)
	}
}

func TestManagedSkillUpdateConvertsReflectOwnershipIrreversibly(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "convert", Description: "d", MainFileContent: "before",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}
	updated, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{
		ID: created.ID, UserID: userID, AgentID: agentID, Scope: "user_agent", ConvertToManual: true,
		Patch: UpdatePatch{Metadata: json.RawMessage(`{"source":"edited","created_by":"reflect"}`)},
		Files: map[string]string{MainFile: "after", "references/note.md": "note"},
	})
	if err != nil {
		t.Fatalf("UpdateManagedSkill convert: %v", err)
	}
	if got := CreatedBy(updated); got != ManualSkillCreatedBy || IsReflectOwned(updated) {
		t.Fatalf("converted ownership = %q, skill=%#v", got, updated)
	}
	if _, err := store.q.GetSkillUsageForUpdate(ctx, storeSkillUsageParams(created.ID, userID, agentID)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("usage after conversion = %v, want no row", err)
	}

	updated, err = store.UpdateManagedSkill(ctx, ManagedSkillUpdate{
		ID: created.ID, UserID: userID, AgentID: agentID, Scope: "user_agent",
		Patch: UpdatePatch{Metadata: json.RawMessage(`{"created_by":"reflect","source":"second-edit"}`)},
	})
	if err != nil {
		t.Fatalf("UpdateManagedSkill second edit: %v", err)
	}
	if got := CreatedBy(updated); got != ManualSkillCreatedBy {
		t.Fatalf("ConvertToManual=false changed owner to %q", got)
	}
	logs, err := store.ListSkillChangelogBySkill(ctx, created.ID, 10)
	if err != nil || len(logs) == 0 || logs[0].Action != "patch" || logs[0].VersionAfter != updated.Version {
		t.Fatalf("update changelog = %#v, %v", logs, err)
	}
	files, err := store.ListFilesWithContent(ctx, created.ID)
	if err != nil || files[MainFile] != "after" || files["references/note.md"] != "note" {
		t.Fatalf("updated files = %#v, %v", files, err)
	}
}

func TestManagedSkillDiskSyncRemovesAndRebuildsMirror(t *testing.T) {
	raw, db, ctx := newTestStore(t)
	userID, _ := seedFixtures(t, db)
	base := t.TempDir()
	store := NewDiskSyncStore(raw, func(scope, agentID string, ownerID string) string { return base })
	created := mustCreateManagedSkill(t, raw, ctx, Skill{Scope: "user", UserID: userID, Name: "mirror", Description: "d"})
	if err := os.MkdirAll(filepath.Join(base, created.Name), 0o755); err != nil {
		t.Fatalf("create mirror directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, created.Name, MainFile), []byte("stale"), 0o644); err != nil {
		t.Fatalf("seed mirror: %v", err)
	}

	if _, err := store.DeprecateManagedSkill(ctx, ManagedSkillDeprecate{ID: created.ID, UserID: userID, Scope: "user", DeprecatedBy: userID}); err != nil {
		t.Fatalf("DeprecateManagedSkill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, created.Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mirror after deprecate stat error = %v, want absent", err)
	}
	if _, err := store.RestoreManagedSkill(ctx, ManagedSkillRestore{ID: created.ID, UserID: userID, Scope: "user", RestoredBy: userID, Now: time.Now().UTC()}); err != nil {
		t.Fatalf("RestoreManagedSkill: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(base, created.Name, MainFile))
	if err != nil || string(content) != "body" {
		t.Fatalf("restored mirror = %q, %v", content, err)
	}
	_, err = store.UpdateManagedSkill(ctx, ManagedSkillUpdate{
		ID: created.ID, UserID: userID, Scope: "system", Files: map[string]string{MainFile: "must-not-write"},
	})
	if !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("failed disk update error = %v, want ErrSkillNotMutable", err)
	}
	content, err = os.ReadFile(filepath.Join(base, created.Name, MainFile))
	if err != nil || string(content) != "body" {
		t.Fatalf("mirror changed after failed DB update = %q, %v", content, err)
	}
	updated, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{
		ID: created.ID, UserID: userID, Scope: "user",
		Files: map[string]string{MainFile: "updated", "references/retained.md": "reference"},
	})
	if err != nil {
		t.Fatalf("UpdateManagedSkill: %v", err)
	}
	content, err = os.ReadFile(filepath.Join(base, updated.Name, MainFile))
	if err != nil || string(content) != "updated" {
		t.Fatalf("updated mirror main = %q, %v", content, err)
	}
	reference, err := os.ReadFile(filepath.Join(base, updated.Name, "references", "retained.md"))
	if err != nil || string(reference) != "reference" {
		t.Fatalf("updated mirror reference = %q, %v", reference, err)
	}
}

func mustCreateManagedSkill(t *testing.T, store *PGStore, ctx context.Context, sk Skill) Skill {
	t.Helper()
	id, err := store.Create(ctx, sk, map[string]string{MainFile: "body"})
	if err != nil {
		t.Fatalf("Create %q: %v", sk.Name, err)
	}
	row, err := store.q.GetSkillByID(ctx, id)
	if err != nil {
		t.Fatalf("GetSkillByID %q: %v", sk.Name, err)
	}
	return mapRow(row)
}

func storeSkillUsageParams(skillID, userID, agentID string) sqlc.GetSkillUsageForUpdateParams {
	return sqlc.GetSkillUsageForUpdateParams{SkillID: skillID, UserID: userID, AgentID: agentID}
}
