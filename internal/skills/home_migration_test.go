package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

func newSkillMigrationFixture(t *testing.T) (*pgxpool.Pool, context.Context, string, *skillMigrationService) {
	t.Helper()
	_, db, ctx := newTestStore(t)
	base := t.TempDir()
	local, err := home.NewLocalStore("local", base)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := NewSkillHomeMigrationService(db, registry)
	if err != nil {
		t.Fatal(err)
	}
	return db, ctx, base, migration
}

func assertNoSkillMarkerOrHome(t *testing.T, db *pgxpool.Pool, ctx context.Context, base string) {
	t.Helper()
	if _, err := sqlc.New(db).GetStorageMigration(ctx, SkillHomeAuthorityMigration); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("Skill marker = %v, want missing", err)
	}
	if _, err := os.Stat(filepath.Join(base, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("Skill Home bytes = %v, want missing", err)
	}
}

func zeroAssetMetadata() []byte {
	h := sha256.Sum256([]byte("stella-mutable-assets-v1\x00"))
	return []byte(`{"layout":"principal_home_data_assets_v1","source_count":0,"source_bytes":0,"source_sha256":"` + hex.EncodeToString(h[:]) + `","target_count":0,"target_bytes":0,"target_sha256":"` + hex.EncodeToString(h[:]) + `"}`)
}

func TestSkillMigrationScanReportsAllInvalidRowsWithBoundedDetails(t *testing.T) {
	_, db, ctx := newTestStore(t)
	local, err := home.NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := NewSkillHomeMigrationService(db, registry)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]sqlc.Skill, 130)
	for i := range rows {
		rows[i] = sqlc.Skill{ID: fmt.Sprintf("invalid-%03d", i), Scope: "system", Name: "bad", Status: SkillStatusActive, Metadata: []byte(`{}`)}
	}
	summary, records, err := migration.scan(ctx, rows, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 || summary.UnsupportedCount != 130 || len(summary.Issues) != maxMigrationIssues {
		t.Fatalf("invalid scan = summary=%+v records=%d", summary, len(records))
	}
	if summary.Issues[0].SkillID != "invalid-000" || summary.Issues[len(summary.Issues)-1].SkillID != "invalid-127" {
		t.Fatalf("issues are not stable: first=%+v last=%+v", summary.Issues[0], summary.Issues[len(summary.Issues)-1])
	}
}

func TestSkillMigrationScanKeepsContextFailuresOperational(t *testing.T) {
	_, db, _ := newTestStore(t)
	local, err := home.NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := NewSkillHomeMigrationService(db, registry)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = migration.scan(ctx, []sqlc.Skill{{ID: "source", Scope: "system", Name: "name", Status: SkillStatusActive, Metadata: []byte(`{}`)}}, nil, nil)
	_, blocked := BlockedSkillMigrationSummary(err)
	if !errors.Is(err, context.Canceled) || blocked {
		t.Fatalf("canceled scan = %v", err)
	}
}

func TestSkillMigrationScanReportsBoundedLogicalPathReason(t *testing.T) {
	_, db, ctx := newTestStore(t)
	seedLegacySkill(t, db, Skill{ID: "bad-path", Scope: "system", Name: "bad-path", Metadata: []byte(`{}`)}, map[string]string{MainFile: "# valid"})
	if _, err := db.Exec(ctx, `UPDATE skill_file SET path = '../outside' WHERE skill_id = 'bad-path' AND path = $1`, MainFile); err != nil {
		t.Fatal(err)
	}
	local, err := home.NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := NewSkillHomeMigrationService(db, registry)
	if err != nil {
		t.Fatal(err)
	}
	row, err := sqlc.New(db).GetSkillByID(ctx, "bad-path")
	if err != nil {
		t.Fatal(err)
	}
	summary, _, err := migration.scan(ctx, []sqlc.Skill{row}, nil, nil)
	if err != nil || len(summary.Issues) != 1 || summary.Issues[0].Reason != "source file path is invalid" || len(summary.Issues[0].Reason) > maxMigrationIssueReason {
		t.Fatalf("path report = %+v, %v", summary, err)
	}
}

func TestSkillMigrationMalformedTargetInspectionIsOperational(t *testing.T) {
	_, db, ctx := newTestStore(t)
	base := t.TempDir()
	local, err := home.NewLocalStore("local", base)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := NewSkillHomeMigrationService(db, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.UseSkillFilesystem(ctx, home.SystemSkillCatalog(), func(sandbox.Filesystem) error { return nil }); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(base, ".agents", "db-skills", "broken")
	if err := os.Symlink("/not-a-managed-skill", entry); err != nil {
		t.Fatal(err)
	}
	_, err = migration.migrationTargetConflict(ctx, home.SystemSkillCatalog(), sandbox.PathWorkspace, "broken", "digest")
	if err == nil {
		t.Fatal("malformed target became a conflict report")
	}
}

func TestSkillMigrationAssetPrerequisiteGate(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		configured  bool
		metadata    []byte
		wantOK      bool
	}{
		{name: "missing"},
		{name: "pending", state: "pending", configured: true, metadata: []byte(`{}`)},
		{name: "not required configured", state: "not_required", configured: true, metadata: []byte(`{}`)},
		{name: "completed unconfigured", state: "completed", configured: false, metadata: []byte(`{}`)},
		{name: "completed malformed metadata", state: "completed", configured: true, metadata: []byte(`{}`)},
		{name: "not required", state: "not_required", metadata: []byte(`{}`), wantOK: true},
		{name: "completed zero assets", state: "completed", configured: true, metadata: zeroAssetMetadata(), wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, ctx, base, migration := newSkillMigrationFixture(t)
			if tc.name != "missing" {
				if _, err := sqlc.New(db).CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: tc.state, ObjectAuthorityConfigured: tc.configured, Metadata: tc.metadata}); err != nil {
					t.Fatal(err)
				}
			}
			summary, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{DryRun: true})
			if tc.wantOK {
				if err != nil || summary.Status != "planned" {
					t.Fatalf("dry run = %+v, %v", summary, err)
				}
			} else if err == nil {
				t.Fatal("unsafe asset prerequisite succeeded")
			}
			assertNoSkillMarkerOrHome(t, db, ctx, base)
		})
	}
}

func TestSkillMigrationSourceReportCollectsRealInvalidRows(t *testing.T) {
	_, db, ctx := newTestStore(t)
	for _, name := range []string{"missing-main", "empty-main", "binary", "bad-path", "many-files", "large-file"} {
		seedLegacySkill(t, db, Skill{ID: name, Scope: "system", Name: name, Metadata: []byte(`{}`)}, map[string]string{MainFile: "# valid"})
	}
	statements := []string{
		`DELETE FROM skill_file WHERE skill_id = 'missing-main' AND path = 'SKILL.md'`,
		`UPDATE skill_file SET content = ''::bytea WHERE skill_id = 'empty-main' AND path = 'SKILL.md'`,
		`UPDATE skill_file SET content = decode('00', 'hex') WHERE skill_id = 'binary' AND path = 'SKILL.md'`,
		`UPDATE skill_file SET path = '../bad' WHERE skill_id = 'bad-path' AND path = 'SKILL.md'`,
		`INSERT INTO skill_file (skill_id, path, content) SELECT 'many-files', 'f' || i, 'x'::bytea FROM generate_series(1, 510) i`,
		`UPDATE skill_file SET content = convert_to(repeat('x', 8388609), 'UTF8') WHERE skill_id = 'large-file' AND path = 'SKILL.md'`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	base := t.TempDir()
	local, err := home.NewLocalStore("local", base)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := NewSkillHomeMigrationService(db, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlc.New(db).CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	summary, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{DryRun: true})
	if _, ok := BlockedSkillMigrationSummary(err); !ok || summary.Status != "blocked" || summary.UnsupportedCount != 6 {
		t.Fatalf("invalid source report = %+v, %v", summary, err)
	}
	assertNoSkillMarkerOrHome(t, db, ctx, base)
}

func TestSkillMigrationTargetConflictPreventsEarlierPublication(t *testing.T) {
	_, db, ctx := newTestStore(t)
	for _, name := range []string{"a-first", "z-later"} {
		seedLegacySkill(t, db, Skill{ID: name, Scope: "system", Name: name, Metadata: []byte(`{}`)}, map[string]string{MainFile: "# " + name})
	}
	base := t.TempDir()
	local, err := home.NewLocalStore("local", base)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.UseSkillFilesystem(ctx, home.SystemSkillCatalog(), func(f sandbox.Filesystem) error {
		return f.Write(ctx, sandbox.PathWorkspace+"/z-later", strings.NewReader("ordinary"), sandbox.WriteOptions{})
	}); err != nil {
		t.Fatal(err)
	}
	migration, err := NewSkillHomeMigrationService(db, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlc.New(db).CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	summary, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{})
	if _, ok := BlockedSkillMigrationSummary(err); !ok || summary.ConflictCount != 1 {
		t.Fatalf("target conflict = %+v, %v", summary, err)
	}
	for _, target := range []string{"a-first", filepath.Join(".stella-migration", "archive", migrationArchiveName("a-first"))} {
		if _, err := os.Lstat(filepath.Join(base, ".agents", "db-skills", target)); !os.IsNotExist(err) {
			t.Fatalf("preflight published %s: %v", target, err)
		}
	}
}

func TestSkillMigrationPostPublicationUsageFailureIsOutcomeUnknown(t *testing.T) {
	_, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	seedLegacySkill(t, db, Skill{ID: "reflect", Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "reflect", Metadata: []byte(`{"created_by":"reflect"}`)}, map[string]string{MainFile: "# reflect"})
	q := sqlc.New(db)
	if _, err := db.Exec(ctx, `INSERT INTO skill_usage (skill_id, user_id, agent_id, use_count, last_used_at) VALUES ('reflect', $1, $2, 1, now())`, userID, agentID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	local, err := home.NewLocalStore("local", base)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := NewSkillHomeMigrationService(db, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE SEQUENCE skill_usage_update_attempts; CREATE FUNCTION reject_skill_usage_logical_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('skill_usage_update_attempts'); IF NEW.scope IS NOT NULL THEN RAISE EXCEPTION 'reject logical usage update'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_skill_usage_logical_update BEFORE UPDATE ON skill_usage FOR EACH ROW EXECUTE FUNCTION reject_skill_usage_logical_update()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DROP TRIGGER IF EXISTS reject_skill_usage_logical_update ON skill_usage; DROP FUNCTION IF EXISTS reject_skill_usage_logical_update(); DROP SEQUENCE IF EXISTS skill_usage_update_attempts`)
	})
	_, err = migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{})
	if !errors.Is(err, sandbox.ErrOutcomeUnknown) {
		t.Fatalf("migration error = %v, want outcome unknown", err)
	}
	var attempts int
	if err := db.QueryRow(ctx, `SELECT last_value FROM skill_usage_update_attempts`).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("update attempts = %d, %v", attempts, err)
	}
	marker, err := q.GetStorageMigration(ctx, SkillHomeAuthorityMigration)
	if err != nil || marker.State != "pending" || string(marker.Metadata) != "{}" {
		t.Fatalf("marker = %+v, %v", marker, err)
	}
	usage, err := q.ListSkillUsageForMigration(ctx)
	if err != nil || len(usage) != 1 || usage[0].Scope.Valid || usage[0].Name.Valid || usage[0].LastContentDigest.Valid {
		t.Fatalf("usage = %+v, %v", usage, err)
	}
	row, err := q.GetSkillByID(ctx, "reflect")
	if err != nil {
		t.Fatal(err)
	}
	record, err := migration.loadRecord(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.verify(ctx, []skillMigrationRecord{record}); err != nil {
		t.Fatalf("published targets not exact: %v", err)
	}
	if _, err := db.Exec(ctx, `DROP TRIGGER reject_skill_usage_logical_update ON skill_usage; DROP FUNCTION reject_skill_usage_logical_update(); DROP SEQUENCE skill_usage_update_attempts`); err != nil {
		t.Fatal(err)
	}
	completed, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{})
	if err != nil || completed.Status != "completed" {
		t.Fatalf("explicit rerun = %+v, %v", completed, err)
	}
	again, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{})
	if err != nil || !reflect.DeepEqual(again, completed) {
		t.Fatalf("idempotent rerun = %+v, %v", again, err)
	}
}

func TestSkillMigrationCompletedUsageDriftIsReadOnly(t *testing.T) {
	_, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	seedLegacySkill(t, db, Skill{ID: "reflect", Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "reflect", Metadata: []byte(`{"created_by":"reflect"}`)}, map[string]string{MainFile: "# reflect"})
	q := sqlc.New(db)
	if _, err := db.Exec(ctx, `INSERT INTO skill_usage (skill_id, user_id, agent_id, use_count, last_used_at) VALUES ('reflect', $1, $2, 1, now())`, userID, agentID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	local, err := home.NewLocalStore("local", base)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := NewSkillHomeMigrationService(db, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{}); err != nil {
		t.Fatal(err)
	}
	driftDigest := strings.Repeat("d", 64)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE skill_usage SET last_content_digest = $1 WHERE skill_id = 'reflect'`, []any{driftDigest}},
		{`UPDATE skill_usage SET use_count = use_count + 1 WHERE skill_id = 'reflect'`, nil},
	} {
		if _, err := db.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
		if _, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{}); err == nil {
			t.Fatalf("completed migration repaired drift after %s", statement.query)
		}
	}
	usage, err := q.ListSkillUsageForMigration(ctx)
	if err != nil || len(usage) != 1 || usage[0].LastContentDigest.String != driftDigest || usage[0].UseCount != 2 {
		t.Fatalf("drifted usage = %+v, %v", usage, err)
	}
	marker, err := q.GetStorageMigration(ctx, SkillHomeAuthorityMigration)
	if err != nil || marker.State != "completed" {
		t.Fatalf("marker = %+v, %v", marker, err)
	}
}

func TestSkillHomeMigrationDryRunThenIdempotent(t *testing.T) {
	_, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	for _, skill := range []Skill{
		{ID: "system", Scope: "system", Name: "base", Description: "x", Metadata: []byte(`{"created_by":"operator"}`)},
		{ID: "system-agent", Scope: "system_agent", AgentID: agentID, Name: "agent", Description: "x", Metadata: []byte(`{"created_by":"operator"}`)},
		{ID: "user", Scope: "user", UserID: userID, Name: "personal", Description: "x", Metadata: []byte(`{"created_by":"operator"}`)},
		{ID: "user-agent", Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "reflect", Description: "x", Metadata: []byte(`{"created_by":"reflect","unicode":"雪"}`)},
		{ID: "old", Scope: "user", UserID: userID, Name: "old", Description: "x", Status: SkillStatusDeprecated, Metadata: []byte(`{}`)},
	} {
		seedLegacySkill(t, db, skill, map[string]string{MainFile: "# " + skill.Name, "references/说明.txt": "内容"})
	}
	if _, err := db.Exec(ctx, `UPDATE skill SET status = 'deprecated' WHERE id = 'old'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO skill_usage (skill_id, user_id, agent_id, use_count, last_used_at) VALUES ('user-agent', $1, $2, 1, now())`, userID, agentID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	local, err := home.NewLocalStore("local", base)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, "local", local)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := NewSkillHomeMigrationService(db, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlc.New(db).CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", ObjectAuthorityConfigured: false, Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	planned, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if planned.Status != "planned" || planned.SourceCount != 5 || planned.ArchiveCount != 5 {
		t.Fatalf("dry run summary: %+v", planned)
	}
	if _, err := os.Stat(filepath.Join(base, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote Home: %v", err)
	}
	completed, err := migration.MigrateSkillHomeAuthority(context.Background(), SkillMigrationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" || completed.ActiveCount != 4 || completed.UsageCount != 1 {
		t.Fatalf("completion summary: %+v", completed)
	}
	usage, err := sqlc.New(db).ListSkillUsageForMigration(ctx)
	if err != nil || len(usage) != 1 {
		t.Fatalf("usage: %v %v", usage, err)
	}
	if !usage[0].Scope.Valid || usage[0].Scope.String != "user_agent" || !usage[0].Name.Valid || usage[0].Name.String != "reflect" || !usage[0].LastContentDigest.Valid {
		t.Fatalf("usage identity: %+v", usage[0])
	}
	if _, err := os.Stat(filepath.Join(base, ".agents", "db-skills", ".stella-migration", "archive")); err != nil {
		t.Fatalf("archive missing: %v", err)
	}
	for _, tc := range []struct{ id, root string }{
		{"system", filepath.Join(base, ".agents", "db-skills")},
		{"system-agent", filepath.Join(base, "agents", agentID, ".agents", "skills")},
		{"user", filepath.Join(base, "users", userID, "data", ".agents", "skills")},
		{"user-agent", filepath.Join(base, "users", userID, "agents", agentID, ".agents", "skills")},
		{"old", filepath.Join(base, "users", userID, "data", ".agents", "skills")},
	} {
		archive := filepath.Join(tc.root, ".stella-migration", "archive", migrationArchiveName(tc.id), "migration.json")
		body, err := os.ReadFile(archive)
		if err != nil || !strings.Contains(string(body), `"id":"`+tc.id+`"`) || !strings.Contains(string(body), `"description":"x"`) {
			t.Fatalf("archive %s = %q, %v", tc.id, body, err)
		}
		if _, err := os.Stat(filepath.Join(tc.root, ".stella-migration", "archive", migrationArchiveName(tc.id), MainFile)); err != nil {
			t.Fatalf("archive main %s: %v", tc.id, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(base, "users", userID, "data", ".agents", "skills", "old")); !os.IsNotExist(err) {
		t.Fatalf("deprecated active link = %v, want absent", err)
	}
	if _, err := os.Stat(filepath.Join(base, ".agents", "db-skills", "personal")); !os.IsNotExist(err) {
		t.Fatalf("user Skill leaked into system catalog: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, ".agents", "db-skills", "reflect")); !os.IsNotExist(err) {
		t.Fatalf("user-agent Skill leaked into system catalog: %v", err)
	}
	again, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, completed) {
		t.Fatalf("rerun = %+v, want %+v", again, completed)
	}
	deleted := filepath.Join(base, ".agents", "db-skills", "base")
	if err := os.Remove(deleted); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{}); err == nil {
		t.Fatal("completed migration repaired deleted active link")
	}
	if _, err := os.Lstat(deleted); !os.IsNotExist(err) {
		t.Fatalf("completed migration recreated active link: %v", err)
	}
	marker, err := sqlc.New(db).GetStorageMigration(ctx, SkillHomeAuthorityMigration)
	if err != nil || marker.State != "completed" {
		t.Fatalf("marker after failed verify = %+v, %v", marker, err)
	}
	if _, err := db.Exec(ctx, `UPDATE storage_migration SET metadata = '{"corrupt":true}'::jsonb WHERE name = $1`, SkillHomeAuthorityMigration); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{DryRun: true}); err == nil {
		t.Fatal("dry run accepted corrupt completed marker")
	}
}
