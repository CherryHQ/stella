package skill

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/plugin"
)

func TestLegacyFileExportPublishesAndArchivesValidatedSelector(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "legacy-skill", "user", false)
	applySkillMigration(t, f)

	prepared, err := f.migrator.store.PrepareLegacyFileExport(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Skills) != 1 {
		t.Fatalf("prepared Skills = %d", len(prepared.Skills))
	}
	entry := &prepared.Skills[0]
	if entry.FileSkillID != fileSkillID("user", f.userID, "", "legacy-skill") || entry.SourceDigest == "" {
		t.Fatalf("prepared identity = %#v", *entry)
	}
	if !bytes.Contains(entry.Files[MainFile].Content, []byte("description: legacy description")) {
		t.Fatalf("frontmatter missing trusted description: %s", entry.Files[MainFile].Content)
	}

	if err := PublishLegacyFileExport(t.Context(), f.manager, prepared); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.base, "users", f.userID, ".agents", "skills", "legacy-skill", MainFile)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, entry.Files[MainFile].Content) {
		t.Fatalf("published SKILL.md differs from prepared bytes")
	}
	if info, err := os.Lstat(filepath.Join(f.base, "users", f.userID, ".agents", "skills", "legacy-skill")); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("published target is not an ordinary directory: %v, %v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(f.base, "users", f.userID, ".agents", "skills", ".stella-legacy-selectors", "legacy-skill")); err != nil {
		t.Fatalf("legacy selector archive missing: %v", err)
	}
	if entry.ContentDigest == "" {
		t.Fatal("published file digest was not returned")
	}

	resources := plugin.NewResourceStore(f.manager)
	resource, err := resources.Get(t.Context(), plugin.ResourceKey{Scope: plugin.ScopeUser, UserID: f.userID, Kind: plugin.ResourceSkill, Name: "legacy-skill"})
	if err != nil {
		t.Fatal(err)
	}
	if resource.Digest != entry.ContentDigest {
		t.Fatalf("resource digest = %q, prepared digest = %q", resource.Digest, entry.ContentDigest)
	}
	if err := PublishLegacyFileExport(t.Context(), f.manager, prepared); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
}

func TestFinalizeLegacyFileEvidenceBridgesTrustedReflectWithoutResettingUsage(t *testing.T) {
	f := newSkillMigrationFixture(t)
	ctx := t.Context()
	f.insertLegacySkill(t, "legacy-reflect", "user_agent", true)
	if _, err := f.migrator.db.Exec(ctx, `UPDATE skill SET disable_model_invocation=false WHERE id='legacy-reflect'`); err != nil {
		t.Fatal(err)
	}
	applySkillMigration(t, f)
	identity, err := f.migrator.store.getIdentityForMigration(ctx, "legacy-reflect")
	if err != nil || identity == nil {
		t.Fatalf("legacy identity = %#v, %v", identity, err)
	}
	oldRevision, err := f.migrator.store.loadIdentityForMigration(ctx, *identity)
	if err != nil {
		t.Fatal(err)
	}
	if !IsReflectOwned(oldRevision.Skill) {
		t.Fatalf("prepared old revision lost Reflect ownership: %#v", oldRevision.Skill)
	}
	if _, err := f.migrator.db.Exec(ctx, `
INSERT INTO skill_changelog(skill_id,user_id,agent_id,scope,action,version_after,content_digest,writer,metadata)
VALUES('legacy-reflect',$1,$2,'user_agent','patch',1,$3,'manual','{}')
`, f.userID, f.agentID, oldRevision.Skill.ContentDigest); err != nil {
		t.Fatal(err)
	}
	usageAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if _, err := f.migrator.db.Exec(ctx, `UPDATE skill_usage SET use_count=7,last_used_at=$1 WHERE skill_id='legacy-reflect'`, usageAt); err != nil {
		t.Fatal(err)
	}

	prepared, err := f.migrator.store.PrepareLegacyFileExport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := PublishLegacyFileExport(ctx, f.manager, prepared); err != nil {
		t.Fatal(err)
	}
	entry := prepared.Skills[0]
	newDigest := strings.TrimPrefix(entry.ContentDigest, "sha256:")
	if newDigest == oldRevision.Skill.ContentDigest || newDigest == "" {
		t.Fatalf("old and filesystem digests unexpectedly match: old=%q new=%q", oldRevision.Skill.ContentDigest, entry.ContentDigest)
	}
	finalize := func() {
		t.Helper()
		tx, err := f.migrator.db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := FinalizeLegacyFileEvidence(ctx, tx, f.manager, prepared); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	finalize()
	finalize()

	var useCount int64
	var lastUsed time.Time
	var usageDigest string
	if err := f.migrator.db.QueryRow(ctx, `SELECT use_count,last_used_at,content_digest FROM skill_usage WHERE skill_id='legacy-reflect'`).Scan(&useCount, &lastUsed, &usageDigest); err != nil {
		t.Fatal(err)
	}
	if useCount != 7 || !lastUsed.Equal(usageAt) || usageDigest != newDigest {
		t.Fatalf("usage after evidence = %d/%v/%q, want 7/%v/%q", useCount, lastUsed, usageDigest, usageAt, newDigest)
	}
	var oldHistory, auditCount int
	if err := f.migrator.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE content_digest=$1), count(*) FILTER (WHERE metadata->>'migration'=$2) FROM skill_changelog WHERE skill_id='legacy-reflect'`, oldRevision.Skill.ContentDigest, legacyFileEvidenceMigration).Scan(&oldHistory, &auditCount); err != nil {
		t.Fatal(err)
	}
	if oldHistory != 1 || auditCount != 1 {
		t.Fatalf("history counts = old %d/audit %d, want 1/1", oldHistory, auditCount)
	}

	store := NewFileStore(f.migrator.db, f.manager)
	active, err := store.ListActiveReflectOwnedUserAgentSkills(ctx, f.userID, f.agentID)
	if err != nil || len(active) != 1 || active[0].ID != entry.FileSkillID || active[0].ContentDigest != newDigest {
		t.Fatalf("active migrated skills = %#v, err=%v", active, err)
	}
	description := "patched after migration"
	patched, err := store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{ID: entry.FileSkillID, UserID: f.userID, AgentID: f.agentID, ExpectedDigest: newDigest, Description: &description})
	if err != nil || patched.Description != description {
		t.Fatalf("patch migrated Skill = %#v, err=%v", patched, err)
	}
	if err := os.MkdirAll(filepath.Join(f.base, "users", f.userID, "agents", f.agentID, ".agents", "resources", "skills", "forged"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.base, "users", f.userID, "agents", f.agentID, ".agents", "resources", "skills", "forged", MainFile), []byte("---\nname: forged\ndescription: forged\nmetadata:\n  created_by: reflect\n---\n# forged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	active, err = store.ListActiveReflectOwnedUserAgentSkills(ctx, f.userID, f.agentID)
	if err != nil || len(active) != 1 || active[0].ID != entry.FileSkillID {
		t.Fatalf("frontmatter forged Reflect ownership = %#v, err=%v", active, err)
	}
}

func TestFinalizeLegacyFileEvidencePreservesRuntimeOwnedUsageAcrossScopes(t *testing.T) {
	f := newSkillMigrationFixture(t)
	ctx := t.Context()
	f.insertLegacySkill(t, "system-runtime-usage", "system", true)
	applySkillMigration(t, f)
	prepared, err := f.migrator.store.PrepareLegacyFileExport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Skills) != 1 || prepared.Skills[0].Scope != "system" {
		t.Fatalf("prepared system Skills = %#v", prepared.Skills)
	}
	if err := PublishLegacyFileExport(ctx, f.manager, prepared); err != nil {
		t.Fatal(err)
	}
	entry := prepared.Skills[0]
	tx, err := f.migrator.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := FinalizeLegacyFileEvidence(ctx, tx, f.manager, prepared); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var resourceID, userID, agentID, digest string
	var useCount int64
	if err := f.migrator.db.QueryRow(ctx, `
SELECT resource_id,user_id::text,agent_id,use_count,content_digest
FROM skill_usage WHERE skill_id='system-runtime-usage'
`).Scan(&resourceID, &userID, &agentID, &useCount, &digest); err != nil {
		t.Fatal(err)
	}
	if resourceID != entry.FileSkillID || userID != f.userID || agentID != f.agentID || useCount != 2 || digest != entry.SourceDigest {
		t.Fatalf("runtime-owned usage = %s/%s/%s/%d/%s, want %s/%s/%s/2/%s", resourceID, userID, agentID, useCount, digest, entry.FileSkillID, f.userID, f.agentID, entry.SourceDigest)
	}
	var audits int
	if err := f.migrator.db.QueryRow(ctx, `SELECT count(*) FROM skill_changelog WHERE resource_id=$1 AND metadata->>'migration'=$2`, entry.FileSkillID, legacyFileEvidenceMigration).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 0 {
		t.Fatalf("untrusted system Skill audits = %d, want 0", audits)
	}
}
