package skill

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestPOSIXReflectCreateAndPatchUseExactRevisionEvidence(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	ctx := t.Context()
	created, err := f.store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID:            f.userID,
		AgentID:           f.agentID,
		Name:              "reflect-exact-evidence",
		Description:       "  created by Reflect  ",
		MainFileContent:   "# Before\n",
		Metadata:          json.RawMessage(`{"source":"review"}`),
		ChangelogMetadata: json.RawMessage(`{"operation":"create"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || !validSkillDigest(created.ContentDigest) || created.Description != "created by Reflect" || !IsReflectOwned(created) {
		t.Fatalf("created Reflect Skill = %#v", created)
	}
	assertReflectRevision(t, f, created, "# Before\n")
	assertReflectEvidence(t, f, created, "create", 1)

	if _, err := f.store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: f.userID, AgentID: f.agentID, Name: created.Name,
		Description: created.Description, MainFileContent: "# Different\n", Metadata: json.RawMessage(`{"source":"review"}`),
	}); !errors.Is(err, ErrSkillDigestConflict) {
		t.Fatalf("same-name different-state create = %v, want digest conflict", err)
	}

	oldLastUsedAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := f.store.db.Exec(ctx, `UPDATE skill_usage SET use_count=7,last_used_at=$1 WHERE skill_id=$2`, oldLastUsedAt, created.ID); err != nil {
		t.Fatal(err)
	}
	description, content := "patched by Reflect", "# After\n"
	patch := ReflectSkillPatch{
		ID: created.ID, UserID: f.userID, AgentID: f.agentID, ExpectedDigest: created.ContentDigest,
		Description: &description, MainFileContent: &content, ChangelogMetadata: json.RawMessage(`{"operation":"patch"}`),
	}
	patched, err := f.store.PatchReflectOwnedUserAgentSkill(ctx, patch)
	if err != nil {
		t.Fatal(err)
	}
	if patched.Version != 2 || patched.ContentDigest == created.ContentDigest || patched.Description != description {
		t.Fatalf("patched Reflect Skill = %#v", patched)
	}
	assertReflectRevision(t, f, patched, content)
	assertReflectEvidence(t, f, patched, "patch", 7)
	oldRevision, err := f.store.LoadExactRevision(ctx, created, created.ContentDigest)
	if err != nil || string(oldRevision.Files[MainFile]) != "# Before\n" {
		t.Fatalf("immutable pre-patch revision = %q, %v", oldRevision.Files[MainFile], err)
	}

	var firstLastUsedAt time.Time
	if err := f.store.db.QueryRow(ctx, `SELECT last_used_at FROM skill_usage WHERE skill_id=$1`, created.ID).Scan(&firstLastUsedAt); err != nil {
		t.Fatal(err)
	}
	retried, err := f.store.PatchReflectOwnedUserAgentSkill(ctx, patch)
	if err != nil || retried.ContentDigest != patched.ContentDigest {
		t.Fatalf("exact stale patch retry = %#v, %v", retried, err)
	}
	var changelogCount int
	var retriedLastUsedAt time.Time
	if err := f.store.db.QueryRow(ctx, `SELECT count(*) FROM skill_changelog WHERE skill_id=$1`, created.ID).Scan(&changelogCount); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRow(ctx, `SELECT last_used_at FROM skill_usage WHERE skill_id=$1`, created.ID).Scan(&retriedLastUsedAt); err != nil {
		t.Fatal(err)
	}
	if changelogCount != 2 || !retriedLastUsedAt.Equal(firstLastUsedAt) {
		t.Fatalf("exact retry changed evidence: changelog=%d last_used_at=%v, want 2/%v", changelogCount, retriedLastUsedAt, firstLastUsedAt)
	}

	different := "# Stale overwrite\n"
	patch.MainFileContent = &different
	if _, err := f.store.PatchReflectOwnedUserAgentSkill(ctx, patch); !errors.Is(err, ErrSkillDigestConflict) {
		t.Fatalf("stale different-state patch = %v, want digest conflict", err)
	}
}

func TestPOSIXReflectRetryCompletesMissingCreateEvidence(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	ctx := t.Context()
	metadata, err := MarkReflectOwnedMetadata(json.RawMessage(`{"source":"recovery"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	desired := Skill{
		ID: "recover1", Scope: "user_agent", UserID: f.userID, AgentID: f.agentID,
		Name: "reflect-create-recovery", Description: "recover evidence", Status: SkillStatusActive,
		Metadata: metadata, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	published, err := f.store.publish(ctx, desired, []revisionFile{{Path: MainFile, Mode: 0o644, Content: []byte("# Recover\n")}}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.q.CreateSkill(ctx, createIdentityParams(desired)); err != nil {
		t.Fatal(err)
	}

	recovered, err := f.store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: f.userID, AgentID: f.agentID, Name: desired.Name,
		Description: desired.Description, MainFileContent: "# Recover\n", Metadata: json.RawMessage(`{"source":"recovery"}`),
		ChangelogMetadata: json.RawMessage(`{"operation":"recovered-create"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != desired.ID || recovered.ContentDigest != published.Skill.ContentDigest {
		t.Fatalf("recovered create = %#v, want %s/%s", recovered, desired.ID, published.Skill.ContentDigest)
	}
	assertReflectEvidence(t, f, recovered, "create", 1)
}

func TestPOSIXReflectPatchRejectsManualOrMissingCurrentAuthority(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	ctx := t.Context()
	manual, err := f.store.CreateManagedSkill(ctx, fixtureSkill(f, "manual-reflect-target", "user_agent"), map[string]string{MainFile: "# Manual\n"})
	if err != nil {
		t.Fatal(err)
	}
	description := "must not patch"
	if _, err := f.store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{
		ID: manual.Skill.ID, UserID: f.userID, AgentID: f.agentID,
		ExpectedDigest: manual.Skill.ContentDigest, Description: &description,
	}); !errors.Is(err, ErrSkillNotReflectOwned) {
		t.Fatalf("manual Reflect patch = %v, want not Reflect-owned", err)
	}

	reflectOwned, err := f.store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: f.userID, AgentID: f.agentID, Name: "missing-reflect-current", MainFileContent: "# Reflect\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.removeSelection(ctx, reflectOwned, reflectOwned.ContentDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{
		ID: reflectOwned.ID, UserID: f.userID, AgentID: f.agentID,
		ExpectedDigest: reflectOwned.ContentDigest, Description: &description,
	}); !IsCurrentSelectorMissing(err) {
		t.Fatalf("Reflect patch without current selector = %v, want selector-missing", err)
	}
	if identity, err := f.store.GetIdentity(ctx, reflectOwned.ID); err != nil || identity == nil {
		t.Fatalf("missing-current patch removed identity: %#v, %v", identity, err)
	}
}

func TestPOSIXReflectRejectsInvalidChangelogBeforeMutation(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	ctx := t.Context()
	if _, err := f.store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: f.userID, AgentID: f.agentID, Name: "invalid-reflect-changelog",
		MainFileContent: "# Invalid\n", ChangelogMetadata: json.RawMessage(`[]`),
	}); err == nil {
		t.Fatal("non-object create changelog metadata was accepted")
	}
	var count int
	if err := f.store.db.QueryRow(ctx, `SELECT count(*) FROM skill WHERE name='invalid-reflect-changelog'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid create committed %d identities: %v", count, err)
	}
}

func assertReflectRevision(t *testing.T, f posixStoreFixture, skill Skill, content string) {
	t.Helper()
	revision, err := f.store.LoadExactRevision(t.Context(), skill, skill.ContentDigest)
	if err != nil || revision.Skill.ContentDigest != skill.ContentDigest || string(revision.Files[MainFile]) != content {
		t.Fatalf("exact Reflect revision = %#v/%q, %v", revision.Skill, revision.Files[MainFile], err)
	}
}

func assertReflectEvidence(t *testing.T, f posixStoreFixture, skill Skill, action string, wantUseCount int64) {
	t.Helper()
	var usageDigest string
	var useCount int64
	if err := f.store.db.QueryRow(t.Context(), `SELECT content_digest,use_count FROM skill_usage WHERE skill_id=$1`, skill.ID).Scan(&usageDigest, &useCount); err != nil {
		t.Fatal(err)
	}
	if usageDigest != skill.ContentDigest || useCount != wantUseCount {
		t.Fatalf("Reflect usage evidence = %s/%d, want %s/%d", usageDigest, useCount, skill.ContentDigest, wantUseCount)
	}
	var exists bool
	if err := f.store.db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM skill_changelog WHERE skill_id=$1 AND action=$2 AND version_after=$3 AND content_digest=$4)`, skill.ID, action, skill.Version, skill.ContentDigest).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("missing %s changelog evidence for %s/%s", action, skill.ID, skill.ContentDigest)
	}
}
