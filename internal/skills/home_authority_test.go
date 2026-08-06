package skills

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestEnsureSkillHomeAuthorityFreshEmptyIsCanonicalAndIdempotent(t *testing.T) {
	db, ctx, base, migration := newSkillMigrationFixture(t)
	q := sqlc.New(db)
	if _, err := q.CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSkillHomeAuthority(ctx, db, migration.homes); err != nil {
		t.Fatal(err)
	}
	marker, err := q.GetStorageMigration(ctx, SkillHomeAuthorityMigration)
	if err != nil || marker.State != "completed" || marker.ObjectAuthorityConfigured || !marker.CompletedAt.Valid || validateSkillMigrationMarker(marker, emptySkillMigrationMetadata()) != nil {
		t.Fatalf("empty marker = %+v, %v", marker, err)
	}
	if err := EnsureSkillHomeAuthority(ctx, db, migration.homes); err != nil {
		t.Fatalf("idempotent ensure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("empty ensure created Home bytes: %v", err)
	}
}

func TestEnsureSkillHomeAuthorityFreshMissRacedWithLegacySourceFailsClosed(t *testing.T) {
	db, ctx, base, migration := newSkillMigrationFixture(t)
	q := sqlc.New(db)
	if _, err := q.CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	err := ensureSkillHomeAuthority(ctx, db, migration.homes, authorityEnsureHooks{beforeFreshLock: func() error {
		_, err := New(db).CreateManagedSkill(ctx, Skill{ID: "raced", Scope: "system", Name: "raced", Metadata: []byte(`{}`)}, map[string]string{MainFile: "# raced"})
		return err
	}})
	if err == nil || !strings.Contains(err.Error(), "migrate-skills") {
		t.Fatalf("raced initialization = %v", err)
	}
	if _, err := q.GetStorageMigration(ctx, SkillHomeAuthorityMigration); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("marker = %v, want missing", err)
	}
	assertNoSkillMarkerOrHome(t, db, ctx, base)
}

func TestEnsureSkillHomeAuthorityFreshCASLoserUsesStrictMarkerPath(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		metadata    []byte
		want        string
	}{
		{name: "pending", state: "pending", metadata: []byte(`{}`), want: "migrate-skills"},
		{name: "completed malformed", state: "completed", metadata: []byte(`{"bad":true}`), want: "verify Home authority"},
		{name: "completed exact", state: "completed", metadata: emptySkillMigrationMetadata()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, ctx, _, migration := newSkillMigrationFixture(t)
			q := sqlc.New(db)
			if _, err := q.CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			err := ensureSkillHomeAuthority(ctx, db, migration.homes, authorityEnsureHooks{beforeFreshLock: func() error {
				_, err := q.CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: SkillHomeAuthorityMigration, State: tc.state, Metadata: tc.metadata})
				return err
			}})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CAS loser error = %v", err)
			}
		})
	}
}

func TestEnsureSkillHomeAuthorityRejectsLegacyRowsWithoutMutation(t *testing.T) {
	db, ctx, base, migration := newSkillMigrationFixture(t)
	q := sqlc.New(db)
	if _, err := q.CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db).CreateManagedSkill(ctx, Skill{ID: "legacy", Scope: "system", Name: "legacy", Metadata: []byte(`{}`)}, map[string]string{MainFile: "# legacy"}); err != nil {
		t.Fatal(err)
	}
	err := EnsureSkillHomeAuthority(ctx, db, migration.homes)
	if err == nil {
		t.Fatal("legacy source accepted")
	}
	if _, err := q.GetStorageMigration(ctx, SkillHomeAuthorityMigration); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("marker = %v, want missing", err)
	}
	assertNoSkillMarkerOrHome(t, db, ctx, base)
}

func TestEnsureSkillHomeAuthorityPendingRequiresExactCanonicalMarker(t *testing.T) {
	for _, tc := range []struct {
		name        string
		configured  bool
		metadata    []byte
		wantPending bool
	}{
		{name: "valid", metadata: []byte(`{}`), wantPending: true},
		{name: "metadata", metadata: []byte(`{"bad":true}`)},
		{name: "object flag", configured: true, metadata: []byte(`{}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, ctx, base, migration := newSkillMigrationFixture(t)
			q := sqlc.New(db)
			if _, err := q.CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := q.CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: SkillHomeAuthorityMigration, State: "pending", ObjectAuthorityConfigured: tc.configured, Metadata: tc.metadata}); err != nil {
				t.Fatal(err)
			}
			err := EnsureSkillHomeAuthority(ctx, db, migration.homes)
			if err == nil {
				t.Fatal("pending marker accepted")
			}
			if tc.wantPending && !strings.Contains(err.Error(), "migrate-skills") {
				t.Fatalf("pending error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(base, ".agents")); !os.IsNotExist(err) {
				t.Fatalf("pending created Home bytes: %v", err)
			}
		})
	}
}
