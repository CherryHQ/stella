package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
	startup, err := f.migrator.ReconcileStartup(t.Context())
	if err != nil || startup.Degraded != nil {
		t.Fatal(errors.Join(err, startup.Degraded))
	}
	return startup.Migration
}

func TestSkillHomeMigrationPublishesScrubsNormalizesAndCompletes(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "migrated-skill", "user_agent", true)
	result := applySkillMigration(t, f)
	if result.State != "completed" || result.SkillCount != 1 || result.FileCount != 2 || !validSkillDigest(result.InventoryDigest) {
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
	if err := f.migrator.verifyCompleted(t.Context()); err != nil {
		t.Fatalf("completion verification: %v", err)
	}
}

func TestSkillStartupReconcileQuarantinesFlatMirrorAndCompletes(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "legacy-flat", "system_agent", false)
	root := filepath.Join(f.base, "agents", f.agentID, ".agents", "skills")
	legacy := filepath.Join(root, "legacy-flat")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, MainFile), []byte("# stale mirror\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := f.migrator.ReconcileStartup(t.Context())
	if err != nil || result.Degraded != nil || result.Migration.State != "completed" {
		t.Fatalf("startup reconciliation = %#v, %v", result, err)
	}
	if info, err := os.Lstat(legacy); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("current selector = %v, %v", info, err)
	}
	quarantined := filepath.Join(root, ".stella-legacy", "legacy-flat", MainFile)
	if content, err := os.ReadFile(quarantined); err != nil || string(content) != "# stale mirror\n" {
		t.Fatalf("quarantined mirror = %q, %v", content, err)
	}
	var files int
	if err := f.migrator.db.QueryRow(t.Context(), "SELECT count(*) FROM skill_file").Scan(&files); err != nil || files != 0 {
		t.Fatalf("legacy PostgreSQL files = %d, %v", files, err)
	}
}

func TestSkillStartupReconcileDegradesOnlyDataConflict(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "legacy-conflict", "system_agent", false)
	root := filepath.Join(f.base, "agents", f.agentID, ".agents", "skills")
	for _, directory := range []string{
		filepath.Join(root, "legacy-conflict"),
		filepath.Join(root, ".stella-legacy", "legacy-conflict"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	result, err := f.migrator.ReconcileStartup(t.Context())
	if err != nil || !errors.Is(result.Degraded, ErrSkillMigrationData) {
		t.Fatalf("data conflict reconciliation = %#v, %v", result, err)
	}
	var files int
	if err := f.migrator.db.QueryRow(t.Context(), "SELECT count(*) FROM skill_file").Scan(&files); err != nil || files != 2 {
		t.Fatalf("degraded reconciliation changed PostgreSQL files = %d, %v", files, err)
	}
	if _, err := f.migrator.store.CreateManagedSkill(t.Context(), Skill{Name: "must-stay-disabled", Scope: "system"}, map[string]string{MainFile: "# blocked"}); !errors.Is(err, ErrManagedSkillsUnavailable) {
		t.Fatalf("degraded reconciliation left writes enabled: %v", err)
	}
}

func TestSkillStartupReconcileRejectsZeroFileLegacyIdentity(t *testing.T) {
	f := newSkillMigrationFixture(t)
	if _, err := f.migrator.db.Exec(t.Context(), `
INSERT INTO skill(id,scope,name,description,status,disable_model_invocation,metadata,version)
VALUES('zero-file','system','zero-file','','active',false,'{}',1)`); err != nil {
		t.Fatal(err)
	}
	result, err := f.migrator.ReconcileStartup(t.Context())
	if err != nil || !errors.Is(result.Degraded, ErrSkillMigrationData) {
		t.Fatalf("zero-file reconciliation = %#v, %v", result, err)
	}
	if _, err := f.migrator.store.GetIdentity(t.Context(), "zero-file"); !errors.Is(err, ErrManagedSkillsUnavailable) {
		t.Fatalf("zero-file source left runtime open: %v", err)
	}
	if _, err := f.migrator.q.GetSkillHomeMigration(t.Context(), skillHomeMigrationID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("zero-file source wrote completion marker: %v", err)
	}
}

func TestSkillStartupReconcileKeepsHomeInfrastructureFailureFatal(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "infrastructure-failure", "system", false)
	if err := f.manager.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := f.migrator.ReconcileStartup(t.Context())
	if err == nil || result.Degraded != nil {
		t.Fatalf("closed Home reconciliation = %#v, %v", result, err)
	}
}

func TestManagedSkillStoreFailsClosedAfterReconciliationDegrades(t *testing.T) {
	f := newSkillMigrationFixture(t)
	cause := errors.New("invalid legacy source")
	f.migrator.store.SetUnavailable(cause)
	if _, err := f.migrator.store.GetIdentity(t.Context(), "any"); !errors.Is(err, ErrManagedSkillsUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("identity availability error = %v", err)
	}
	if _, err := f.migrator.store.CreateManagedSkill(t.Context(), Skill{Name: "blocked", Scope: "system"}, map[string]string{MainFile: "# blocked"}); !errors.Is(err, ErrManagedSkillsUnavailable) {
		t.Fatalf("write availability error = %v", err)
	}
}

func TestManagedSkillStoreUnavailableUntilStartupReconciliationCompletes(t *testing.T) {
	f := newSkillMigrationFixture(t)
	f.insertLegacySkill(t, "startup-gated", "system", false)
	f.migrator.store.BeginStartupReconciliation()
	if _, err := f.migrator.store.CreateManagedSkill(t.Context(), Skill{Name: "too-early", Scope: "system"}, map[string]string{MainFile: "# blocked"}); !errors.Is(err, ErrManagedSkillsUnavailable) || !errors.Is(err, ErrManagedSkillsPending) {
		t.Fatalf("runtime write before reconciliation = %v", err)
	}
	var identities int
	if err := f.migrator.db.QueryRow(t.Context(), "SELECT count(*) FROM skill WHERE id='too-early'").Scan(&identities); err != nil || identities != 0 {
		t.Fatalf("runtime write raced inventory: count=%d err=%v", identities, err)
	}
	result, err := f.migrator.ReconcileStartup(t.Context())
	if err != nil || result.Degraded != nil {
		t.Fatalf("startup reconciliation = %#v, %v", result, err)
	}
	if _, err := f.migrator.store.CreateManagedSkill(t.Context(), Skill{Name: "after-cutover", Scope: "system"}, map[string]string{MainFile: "# available"}); err != nil {
		t.Fatalf("runtime write after reconciliation: %v", err)
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
			result, err := f.migrator.ReconcileStartup(context.Background())
			errs <- errors.Join(err, result.Degraded)
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
				result, err := f.migrator.ReconcileStartup(t.Context())
				return errors.Join(err, result.Degraded)
			},
		},
		{name: "empty startup marker", populate: func(*testing.T, skillMigrationFixture) {}, run: func(t *testing.T, f skillMigrationFixture) error {
			result, err := f.migrator.ReconcileStartup(t.Context())
			return errors.Join(err, result.Degraded)
		}},
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
	result, err := f.migrator.ReconcileStartup(t.Context())
	err = errors.Join(err, result.Degraded)
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
	if err := f.migrator.verifyCompleted(t.Context()); err != nil {
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
			if err := f.migrator.verifyCompleted(t.Context()); err != nil {
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
	if err := f.migrator.verifyCompleted(t.Context()); err == nil || !strings.Contains(err.Error(), "verify current Skill") {
		t.Fatalf("missing live current authority = %v", err)
	}
}

func TestSkillHomeMigrationCompletedVerificationAllowsManagedUpdate(t *testing.T) {
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
	description := "valid managed update"
	updated, err := f.migrator.store.UpdateManagedSkill(t.Context(), ManagedSkillUpdate{
		ID:             before.Skill.ID,
		UserID:         before.Skill.UserID,
		AgentID:        before.Skill.AgentID,
		Scope:          before.Skill.Scope,
		Patch:          UpdatePatch{Description: &description},
		ExpectedDigest: before.Skill.ContentDigest,
	})
	if err != nil || updated.Skill.ContentDigest == before.Skill.ContentDigest {
		t.Fatalf("managed update = %q, %v", updated.Skill.ContentDigest, err)
	}
	if err := f.migrator.verifyCompleted(t.Context()); err != nil {
		t.Fatalf("completed verification after managed update: %v", err)
	}
}
