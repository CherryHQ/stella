package resourceupgrade

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/skill"
)

func TestFilesystemMigrationRestartsAtEachBoundary(t *testing.T) {
	for _, boundary := range []string{"prepared", "published", "evidence", "settings-drift"} {
		t.Run(boundary, func(t *testing.T) {
			db := dbtest.New(t)
			ctx := t.Context()
			base := t.TempDir()
			roots, err := home.NewWorkspaceManager(db, base)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = roots.Close() })
			// A deprecated Skill adds settings.json to the write set, so this
			// exercises both selector archival and a changed settings CAS digest.
			if _, err := db.Exec(ctx, `INSERT INTO skill(id,scope,name,description,status,version) VALUES('old-id','system','migrated','Migration fixture','deprecated',1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `INSERT INTO skill_file(skill_id,path,content) VALUES('old-id','SKILL.md',$1)`, []byte("# Migrated\n")); err != nil {
				t.Fatal(err)
			}
			legacySkills, err := skill.NewLegacySkillStore(db, roots)
			if err != nil {
				t.Fatal(err)
			}
			oldMigration, err := skill.NewSkillHomeMigratorFromStore(db, legacySkills)
			if err != nil {
				t.Fatal(err)
			}
			startup, err := oldMigration.ReconcileStartup(ctx)
			if err != nil || startup.Degraded != nil {
				t.Fatal(errors.Join(err, startup.Degraded))
			}
			if err := os.WriteFile(filepath.Join(base, ".agents", "settings.json"), []byte(`{"disabled_tools":{"plugin:existing":["z/a","a/z"]}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			legacyPlugins := plugin.NewLegacyService(db, nil, nil, nil)
			mcpService := mcp.NewServiceForPool(db, nil, nil)
			interrupted := errors.New("simulated process interruption")
			err = Run(ctx, Dependencies{DB: db, Roots: roots, LegacyPlugins: legacyPlugins, LegacySkills: legacySkills, MCPService: mcpService, Checkpoint: func(stage string) error {
				if stage == boundary || (boundary == "settings-drift" && stage == "prepared") {
					return interrupted
				}
				return nil
			}})
			// The coordinator is intentionally tested through its exported
			// composition boundary; the manifest remains private to this package.
			if !errors.Is(err, interrupted) {
				t.Fatalf("interruption = %v", err)
			}
			state, err := ReadState(ctx, db)
			if err != nil || state != State(statePrepared) {
				t.Fatalf("interrupted state = %q, %v", state, err)
			}
			if boundary == "settings-drift" {
				resources := plugin.NewResourceStore(roots)
				_, digest, err := resources.ReadSettings(ctx, plugin.ScopeSystem, "", "")
				if err != nil {
					t.Fatal(err)
				}
				key := plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceSkill, Name: "user-edit"}
				if _, _, err := resources.SetDisabled(ctx, key, digest, true); err != nil {
					t.Fatal(err)
				}
				if err := Run(ctx, Dependencies{DB: db, Roots: roots, LegacyPlugins: legacyPlugins, LegacySkills: legacySkills, MCPService: mcpService}); err == nil {
					t.Fatal("accepted settings drift")
				}
				settings, _, err := resources.ReadSettings(ctx, plugin.ScopeSystem, "", "")
				if err != nil || len(settings.Disabled) != 1 || settings.Disabled[0] != "skill:user-edit" {
					t.Fatalf("overwrote user settings: %#v, %v", settings, err)
				}
				return
			}
			if err := Run(ctx, Dependencies{DB: db, Roots: roots, LegacyPlugins: legacyPlugins, LegacySkills: legacySkills, MCPService: mcpService}); err != nil {
				t.Fatalf("restart: %v", err)
			}
			state, err = ReadState(ctx, db)
			if err != nil || !state.Completed() {
				t.Fatalf("completed = %v, %v", state, err)
			}
			path := filepath.Join(base, ".agents", "skills", "migrated", "SKILL.md")
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			settings, _, err := plugin.NewResourceStore(roots).ReadSettings(ctx, plugin.ScopeSystem, "", "")
			if err != nil || len(settings.Disabled) != 1 || settings.Disabled[0] != "skill:migrated" {
				t.Fatalf("settings = %#v, %v", settings, err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			// Completed startup never resurrects deleted files from retained rows.
			if err := Run(ctx, Dependencies{DB: db}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("deleted file resurrected: %v", err)
			}
		})
	}
}
