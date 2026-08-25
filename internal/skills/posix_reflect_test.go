package skills

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
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

func TestPOSIXReflectMutationsRejectStaleAgentRun(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	ctx := t.Context()
	target, err := f.store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: f.userID, AgentID: f.agentID, Name: "reflect-fenced-target",
		Description: "before", MainFileContent: "# Before\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	stale := staleReflectAgentRunContext(t, f)
	if _, err := f.store.CreateReflectOwnedUserAgentSkill(stale, ReflectSkillCreate{
		UserID: f.userID, AgentID: f.agentID, Name: "reflect-stale-create",
		MainFileContent: "# Stale create\n",
	}); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale Reflect create error = %v, want ErrLeaseLost", err)
	}
	var createdRows int
	if err := f.store.db.QueryRow(ctx, `SELECT count(*) FROM skill WHERE name = 'reflect-stale-create'`).Scan(&createdRows); err != nil || createdRows != 0 {
		t.Fatalf("stale Reflect create rows = %d, err=%v", createdRows, err)
	}

	description := "stale patch"
	content := "# Stale patch\n"
	if _, err := f.store.PatchReflectOwnedUserAgentSkill(stale, ReflectSkillPatch{
		ID: target.ID, UserID: f.userID, AgentID: f.agentID, ExpectedDigest: target.ContentDigest,
		Description: &description, MainFileContent: &content,
	}); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale Reflect patch error = %v, want ErrLeaseLost", err)
	}
	current, err := f.store.loadIdentity(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if current.Skill.ContentDigest != target.ContentDigest || current.Skill.Description != target.Description {
		t.Fatalf("stale Reflect patch changed current revision: %#v", current.Skill)
	}

	var lastUsedAt time.Time
	if err := f.store.db.QueryRow(ctx, `SELECT last_used_at FROM skill_usage WHERE skill_id = $1`, target.ID).Scan(&lastUsedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DeleteReflectOwnedUserAgentSkill(stale, ReflectSkillDelete{
		ID: target.ID, UserID: f.userID, AgentID: f.agentID,
		ExpectedDigest: target.ContentDigest, ExpectedUsageLastUsedAt: lastUsedAt,
	}); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale Reflect delete error = %v, want ErrLeaseLost", err)
	}
	if identity, err := f.store.GetIdentity(ctx, target.ID); err != nil || identity == nil {
		t.Fatalf("stale Reflect delete removed identity: %#v, %v", identity, err)
	}
}

func staleReflectAgentRunContext(t *testing.T, f posixStoreFixture) context.Context {
	t.Helper()
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := f.store.q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: uuid.NewString(), SessionID: sessionID, Channel: "reflect", Kind: "task",
		LastActive: time.Now().UTC(), AgentID: pgtype.Text{String: f.agentID, Valid: true},
		UserID: pgtype.Text{String: f.userID, Valid: true},
	}); err != nil {
		t.Fatalf("create Reflect AgentRun session: %v", err)
	}
	bootID := agentrun.NewBootID()
	if _, err := f.store.q.CreateExecutorBoot(ctx, sqlc.CreateExecutorBootParams{ID: bootID}); err != nil {
		t.Fatalf("create Reflect executor boot: %v", err)
	}
	runs := agentrun.NewStore(f.store.db, bootID)
	lease, err := runs.Acquire(ctx, sessionID, "reflect")
	if err != nil {
		t.Fatalf("acquire Reflect AgentRun: %v", err)
	}
	stale, ok := agentrun.InheritGuard(ctx, lease.Context())
	if !ok {
		t.Fatal("inherit Reflect AgentRun guard")
	}
	if err := lease.Finish(ctx, agentrun.StatusCompleted, ""); err != nil {
		t.Fatalf("finish Reflect AgentRun: %v", err)
	}
	return stale
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
