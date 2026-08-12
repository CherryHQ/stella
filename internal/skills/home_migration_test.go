package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
)

type skillMigrationFixture struct {
	migrator *SkillHomeMigrator
	manager  *home.WorkspaceManager
	base     string
	userID   string
	agentID  string
}

func newSkillMigrationFixture(t *testing.T) skillMigrationFixture {
	t.Helper()
	db := dbtest.New(t)
	userID, agentID := uuid.NewString(), "migration-agent"
	if _, err := db.Exec(t.Context(), "INSERT INTO auth_user(id,email) VALUES($1,$2)", userID, userID+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), "INSERT INTO agent(id,name,workspace) VALUES($1,'Migration Agent','')", agentID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	migrator, err := NewSkillHomeMigrator(db, manager)
	if err != nil {
		t.Fatal(err)
	}
	return skillMigrationFixture{migrator: migrator, manager: manager, base: base, userID: userID, agentID: agentID}
}

func (f skillMigrationFixture) insertLegacySkill(t *testing.T, id, scope string, withUsage bool) {
	t.Helper()
	var userID, agentID any
	switch scope {
	case "user":
		userID = f.userID
	case "user_agent":
		userID, agentID = f.userID, f.agentID
	case "system_agent":
		agentID = f.agentID
	}
	if _, err := f.migrator.db.Exec(t.Context(), `
INSERT INTO skill(id,scope,user_id,agent_id,name,description,status,disable_model_invocation,metadata,version)
VALUES($1,$2,$3,$4,$1,'legacy description','active',true,'{"created_by":"reflect"}',7)`, id, scope, userID, agentID); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{MainFile: []byte("# Legacy\n"), "scripts/run.sh": []byte("#!/bin/sh\nprintf migrated")} {
		if _, err := f.migrator.db.Exec(t.Context(), "INSERT INTO skill_file(skill_id,path,content) VALUES($1,$2,$3)", id, path, content); err != nil {
			t.Fatal(err)
		}
	}
	if withUsage {
		if _, err := f.migrator.db.Exec(t.Context(), "INSERT INTO skill_usage(skill_id,user_id,agent_id,use_count) VALUES($1,$2,$3,2)", id, f.userID, f.agentID); err != nil {
			t.Fatal(err)
		}
	}
}

func applySkillMigration(t *testing.T, f skillMigrationFixture) SkillHomeMigrationResult {
	t.Helper()
	result, err := f.migrator.Migrate(t.Context(), SkillHomeMigrationOptions{Apply: true, ConfirmWritersStopped: true, ConfirmBackupVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSkillHomeMigrationDryRunMakesNoAuthorityOrMarkerWrites(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "dry-run-skill", "user_agent", false)

	first, err := f.migrator.Migrate(t.Context(), SkillHomeMigrationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.migrator.Migrate(t.Context(), SkillHomeMigrationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !first.DryRun || first.State != "planned" || !sameSkillMigrationInventory(first, second) || first.SkillCount != 1 || first.FileCount != 2 {
		t.Fatalf("dry-run results = %#v, %#v", first, second)
	}
	if _, err := f.migrator.q.GetSkillHomeMigration(t.Context(), skillHomeMigrationID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("dry-run marker = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.base, "users", f.userID, ".agents", "agent-skills", f.agentID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run materialized Home authority: %v", err)
	}
	var files int
	if err := f.migrator.db.QueryRow(t.Context(), "SELECT count(*) FROM skill_file").Scan(&files); err != nil || files != 2 {
		t.Fatalf("legacy files after dry-run = %d, %v", files, err)
	}
}

func TestSkillHomeMigrationPublishesScrubsNormalizesAndCompletes(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "migrated-skill", "user_agent", true)
	result := applySkillMigration(t, f)
	if result.State != "completed" || result.DryRun || result.SkillCount != 1 || result.FileCount != 2 || !validSkillDigest(result.InventoryDigest) {
		t.Fatalf("apply result = %#v", result)
	}
	identity, err := f.migrator.store.GetIdentity(t.Context(), "migrated-skill")
	if err != nil || identity == nil {
		t.Fatalf("identity = %#v, %v", identity, err)
	}
	revision, err := f.migrator.store.LoadCurrentRevision(t.Context(), *identity)
	if err != nil || string(revision.Files["scripts/run.sh"]) != "#!/bin/sh\nprintf migrated" {
		t.Fatalf("Home revision = %#v, %v", revision, err)
	}
	var fileCount, version int64
	var description, status, metadata, usageDigest string
	var disabled bool
	if err := f.migrator.db.QueryRow(t.Context(), `
SELECT (SELECT count(*) FROM skill_file), description, status, disable_model_invocation,
       metadata::text, version, (SELECT content_digest FROM skill_usage WHERE skill_id=skill.id)
FROM skill WHERE id='migrated-skill'`).Scan(&fileCount, &description, &status, &disabled, &metadata, &version, &usageDigest); err != nil {
		t.Fatal(err)
	}
	if fileCount != 0 || description != "" || status != SkillStatusActive || disabled || metadata != "{}" || version != 1 || usageDigest != revision.Skill.ContentDigest {
		t.Fatalf("cutover state files=%d description=%q status=%q disabled=%v metadata=%q version=%d usage=%q digest=%q", fileCount, description, status, disabled, metadata, version, usageDigest, revision.Skill.ContentDigest)
	}
	if rerun := applySkillMigration(t, f); !sameSkillMigrationInventory(result, rerun) || rerun.State != "completed" {
		t.Fatalf("completed rerun = %#v", rerun)
	}
	if err := f.migrator.EnsureReady(t.Context()); err != nil {
		t.Fatalf("startup verification: %v", err)
	}
}

func TestSkillHomeMigrationResumesPublishedRevisionAndSerializesCompletedRerun(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "resume-skill", "user", false)
	sources, _, err := f.migrator.inventory(t.Context())
	if err != nil || len(sources) != 1 {
		t.Fatalf("inventory = %d, %v", len(sources), err)
	}
	if err := f.migrator.publishSource(t.Context(), sources[0]); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			_, err := f.migrator.Migrate(context.Background(), SkillHomeMigrationOptions{Apply: true, ConfirmWritersStopped: true, ConfirmBackupVerified: true})
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("serialized migration = %v", err)
		}
	}
}

func TestSkillHomeMigrationReconcilesUnknownMarkerAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name     string
		populate func(*testing.T, skillMigrationFixture)
		run      func(*testing.T, skillMigrationFixture) error
	}{
		{
			name: "nonempty apply",
			populate: func(t *testing.T, f skillMigrationFixture) {
				f.insertLegacySkill(t, "unknown-marker", "system_agent", false)
			},
			run: func(t *testing.T, f skillMigrationFixture) error {
				_, err := f.migrator.Migrate(t.Context(), SkillHomeMigrationOptions{Apply: true, ConfirmWritersStopped: true, ConfirmBackupVerified: true})
				return err
			},
		},
		{name: "empty startup marker", populate: func(*testing.T, skillMigrationFixture) {}, run: func(t *testing.T, f skillMigrationFixture) error { return f.migrator.EnsureReady(t.Context()) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSkillMigrationFixture(t)
			test.populate(t, f)
			f.migrator.commit = func(ctx context.Context, tx pgx.Tx) error {
				if err := tx.Commit(ctx); err != nil {
					return err
				}
				return errors.New("injected acknowledgement loss")
			}
			if err := test.run(t, f); err != nil {
				t.Fatalf("acknowledged by exact readback = %v", err)
			}
			if err := f.migrator.verifyCompleted(t.Context()); err != nil {
				t.Fatalf("durable completion = %v", err)
			}
		})
	}
}

func TestSkillHomeMigrationRejectsDifferentSelfConsistentMarkerAfterUnknownCommit(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "wrong-marker", "system", false)
	f.migrator.commit = func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		_, err := f.migrator.db.Exec(ctx, `
UPDATE skill_home_migration
SET source_skill_count=0, source_file_count=0, source_content_bytes=0,
    source_inventory_digest=$1, inventory='[]'::jsonb`, emptySkillInventoryDigest)
		return errors.Join(errors.New("injected acknowledgement loss"), err)
	}
	_, err := f.migrator.Migrate(t.Context(), SkillHomeMigrationOptions{Apply: true, ConfirmWritersStopped: true, ConfirmBackupVerified: true})
	if !errors.Is(err, ErrMarkerOutcomeUnknown) || !strings.Contains(err.Error(), "differs from attempted inventory") {
		t.Fatalf("different completion marker = %v", err)
	}
}

func TestSkillHomeMigrationPreservesLegacyNameOutsidePathGrammar(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "legacy-name", "user", false)
	if _, err := f.migrator.db.Exec(t.Context(), "UPDATE skill SET name=$1 WHERE id='legacy-name'", "Legacy / mixed name"); err != nil {
		t.Fatal(err)
	}
	result := applySkillMigration(t, f)
	if result.SkillCount != 1 {
		t.Fatalf("migration result = %#v", result)
	}
	if err := f.migrator.EnsureReady(t.Context()); err != nil {
		t.Fatalf("completed legacy-name verification: %v", err)
	}
	identity, err := f.migrator.store.GetIdentity(t.Context(), "legacy-name")
	if err != nil || identity == nil || identity.Name != "Legacy / mixed name" {
		t.Fatalf("legacy identity = %#v, %v", identity, err)
	}
}

func TestSkillHomeMigrationCompletedVerificationHonorsCurrentAuthorityAndOwnerDeletion(t *testing.T) {
	for _, scope := range []string{"user", "system_agent"} {
		t.Run(scope, func(t *testing.T) {
			f := newSkillMigrationFixture(t)
			f.insertLegacySkill(t, "lifecycle-"+scope, scope, false)
			applySkillMigration(t, f)
			if scope == "user" {
				_, err := f.migrator.db.Exec(t.Context(), "DELETE FROM auth_user WHERE id=$1", f.userID)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := f.migrator.db.Exec(t.Context(), "DELETE FROM agent WHERE id=$1", f.agentID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := f.migrator.EnsureReady(t.Context()); err != nil {
				t.Fatalf("completed rerun after owner deletion: %v", err)
			}
		})
	}

	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "missing-current", "user_agent", false)
	applySkillMigration(t, f)
	selector := filepath.Join(f.base, "users", f.userID, ".agents", "agent-skills", f.agentID, "missing-current")
	if err := os.Remove(selector); err != nil {
		t.Fatal(err)
	}
	if err := f.migrator.EnsureReady(t.Context()); err == nil || !strings.Contains(err.Error(), "verify current Skill") {
		t.Fatalf("missing live current authority = %v", err)
	}
}

func TestSkillHomeMigrationCompletedVerificationRejectsDifferentCurrentRevision(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "changed-current", "user_agent", false)
	applySkillMigration(t, f)
	identity, err := f.migrator.store.GetIdentity(t.Context(), "changed-current")
	if err != nil || identity == nil {
		t.Fatalf("migrated identity = %#v, %v", identity, err)
	}
	before, err := f.migrator.store.loadIdentity(t.Context(), *identity)
	if err != nil {
		t.Fatal(err)
	}
	after := before.Skill
	after.Description = "valid second revision"
	after.Version++
	after.UpdatedAt = after.UpdatedAt.Add(time.Nanosecond)
	published, err := f.migrator.store.publish(t.Context(), after, before.Files, before.Skill.ContentDigest, false)
	if err != nil || published.Skill.ContentDigest == before.Skill.ContentDigest {
		t.Fatalf("publish second current revision = %q, %v", published.Skill.ContentDigest, err)
	}
	if err := f.migrator.EnsureReady(t.Context()); err == nil || !strings.Contains(err.Error(), "verify current Skill") {
		t.Fatalf("different live current authority = %v", err)
	}
}

func TestSkillHomeMigrationRequiresApplyAttestationsAndBlocksNonemptyStartup(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "blocked-skill", "system", false)
	if _, err := f.migrator.Migrate(t.Context(), SkillHomeMigrationOptions{Apply: true}); err == nil || !strings.Contains(err.Error(), "are required") {
		t.Fatalf("missing attestations = %v", err)
	}
	if err := f.migrator.EnsureReady(t.Context()); err == nil || !strings.Contains(err.Error(), "storage migrate-skills") {
		t.Fatalf("nonempty startup gate = %v", err)
	}
}
