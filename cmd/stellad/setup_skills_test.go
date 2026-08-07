package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func newHomeSkillAuthorityFixture(t *testing.T) (context.Context, *pgxpool.Pool, *sqlc.Queries, *home.Registry) {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("STELLA_HOME", homeDir)
	store, err := home.NewLocalStore("local", homeDir)
	if err != nil {
		t.Fatal(err)
	}
	db := dbtest.New(t)
	registry, err := home.NewRegistry(db, store.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := sqlc.New(db)
	if _, err := q.CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{
		Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	return ctx, db, q, registry
}

func TestHomeSkillAuthorityCompositionFreshMarkerUsesOnlyHomeCurrentState(t *testing.T) {
	ctx, db, q, registry := newHomeSkillAuthorityFixture(t)
	if err := skills.EnsureSkillHomeAuthority(ctx, db, registry); err != nil {
		t.Fatalf("EnsureSkillHomeAuthority: %v", err)
	}
	authority, err := setupHomeSkillAuthority(ctx, db, registry)
	if err != nil {
		t.Fatalf("setupHomeSkillAuthority: %v", err)
	}

	created, err := authority.store.CreateManagedSkill(ctx, skills.Skill{
		Scope: "user", UserID: "user-1", Name: "home-only", Description: "Home catalog",
	}, map[string]string{skills.MainFile: "---\nname: home-only\ndescription: Home catalog\n---\nbody\n"})
	if err != nil {
		t.Fatalf("CreateManagedSkill: %v", err)
	}
	if created.Skill.ContentDigest == "" {
		t.Fatal("created Home Skill has no content digest")
	}
	if err := skills.EnsureSkillHomeAuthority(ctx, db, registry); err != nil {
		t.Fatalf("completed marker did not verify Home catalog data: %v", err)
	}
	loaded, err := authority.store.Get(ctx, created.Skill.ID)
	if err != nil || loaded.ContentDigest != created.Skill.ContentDigest {
		t.Fatalf("Home catalog Get = %#v, %v", loaded, err)
	}
	updated, err := authority.store.UpdateManagedSkill(ctx, skills.ManagedSkillUpdate{
		ID: created.Skill.ID, Scope: created.Skill.Scope, UserID: created.Skill.UserID,
		ExpectedDigest: created.Skill.ContentDigest,
		Files:          map[string]string{"note.txt": "Home owns this too"},
	})
	if err != nil || updated.Skill.ContentDigest == created.Skill.ContentDigest {
		t.Fatalf("Home update = %#v, %v", updated, err)
	}
	if err := authority.store.DeleteManagedSkill(ctx, skills.ManagedSkillDelete{
		ID: updated.Skill.ID, Scope: updated.Skill.Scope, UserID: updated.Skill.UserID, ExpectedDigest: updated.Skill.ContentDigest,
	}); err != nil {
		t.Fatalf("Home delete: %v", err)
	}

	rows, err := q.ListAllSkills(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("legacy skill rows = %d, %v; want none", len(rows), err)
	}
	var changelogRows int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM skill_changelog").Scan(&changelogRows); err != nil || changelogRows != 0 {
		t.Fatalf("legacy skill_changelog rows = %d, %v; want none", changelogRows, err)
	}
}

func TestHomeSkillAuthorityMarkerGateRejectsLegacyBeforeConstruction(t *testing.T) {
	ctx, db, q, registry := newHomeSkillAuthorityFixture(t)
	if _, err := skills.New(db).CreateManagedSkill(ctx, skills.Skill{ID: "legacy", Scope: "system", Name: "legacy"}, map[string]string{skills.MainFile: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := skills.EnsureSkillHomeAuthority(ctx, db, registry); err == nil {
		t.Fatal("legacy current state passed Home authority gate")
	}
	if _, err := q.GetStorageMigration(ctx, skills.SkillHomeAuthorityMigration); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("marker = %v, want missing", err)
	}
}

func TestProductionRevisionTelemetryWiringUsesNonzeroThresholds(t *testing.T) {
	if productionRevisionWarningCount <= 0 || productionRevisionWarningBytes <= 0 || productionRevisionScanEntries <= 8*(512+2) || productionRevisionScanBytes <= productionRevisionWarningBytes {
		t.Fatalf("production retained-revision thresholds/limits are unreachable: count=%d bytes=%d entries=%d scan_bytes=%d", productionRevisionWarningCount, productionRevisionWarningBytes, productionRevisionScanEntries, productionRevisionScanBytes)
	}
	source, err := os.ReadFile("setup_skills.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"skills.NewRevisionTelemetry", "skills.NewHomeSkillPublisherWithRevisionTelemetry", "catalog.ObserveRetainedRevisions", "skills.HomeSkillObservationCatalogRoot"} {
		if !strings.Contains(string(source), required) {
			t.Errorf("production retained-revision telemetry is missing %q", required)
		}
	}
}

// TestProductionSkillAuthorityCompositionBoundary checks this composition root,
// not migration-only packages: it must gate before the first Store consumer and
// must not instantiate legacy current state or the legacy SQL Skill curator.
func TestProductionSkillAuthorityCompositionBoundary(t *testing.T) {
	source, err := os.ReadFile("commands.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "commands.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]token.Pos{}
	forbidden := map[string]bool{"skills.New": false, "reflect.NewSQLUsageCuratorStoreForPool": false, "materializeDBSkill": false}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callName(call.Fun)
		if _, ok := forbidden[name]; ok {
			forbidden[name] = true
		}
		if name == "skills.EnsureSkillHomeAuthority" || name == "setupHomeSkillAuthority" || name == "setupPlugins" {
			positions[name] = call.Pos()
		}
		return true
	})
	for name, found := range forbidden {
		if found {
			t.Errorf("production composition constructs forbidden %s", name)
		}
	}
	if positions["skills.EnsureSkillHomeAuthority"] == token.NoPos || positions["setupHomeSkillAuthority"] == token.NoPos || positions["setupPlugins"] == token.NoPos {
		t.Fatalf("required composition calls missing: %#v", positions)
	}
	if positions["skills.EnsureSkillHomeAuthority"] >= positions["setupHomeSkillAuthority"] || positions["setupHomeSkillAuthority"] >= positions["setupPlugins"] {
		t.Fatalf("Home authority gate/construction must precede first Store consumer: %#v", positions)
	}
	if strings.Contains(string(source), "NewSQLUsageCuratorStoreForPool(db)") {
		t.Fatal("production composition retains SQL Skill usage curator")
	}
}

func callName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return callName(value.X) + "." + value.Sel.Name
	default:
		return ""
	}
}
