package db

import (
	"context"
	"io/fs"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestOpenDBFreshInstallDoesNotCreateFeishuTokensTable(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)

	if tableExists(t, db, "feishu_tokens") {
		t.Fatal("feishu_tokens table should not exist after fresh install migrations")
	}
}

func TestGoalAttemptRepairRoundsMigrationDownUp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	sub, err := fs.Sub(MigrationsFS, "migrations")
	if err != nil {
		t.Fatalf("open migrations fs: %v", err)
	}
	sqlDB := stdlib.OpenDBFromPool(db)
	defer func() { _ = sqlDB.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.DownTo(ctx, 20260702085628); err != nil {
		t.Fatalf("goose down repair_rounds migration: %v", err)
	}
	if columnExists(t, db, "agent_goal_attempt", "repair_rounds") {
		t.Fatal("repair_rounds should not exist after down")
	}
	if _, err := provider.UpTo(ctx, 20260702110624); err != nil {
		t.Fatalf("goose up repair_rounds migration: %v", err)
	}
	if !columnExists(t, db, "agent_goal_attempt", "repair_rounds") {
		t.Fatal("repair_rounds should exist after up")
	}
}

func TestGoalFailureResponsibilityMigrationMapsAndRestoresClasses(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	sub, err := fs.Sub(MigrationsFS, "migrations")
	if err != nil {
		t.Fatalf("open migrations fs: %v", err)
	}
	sqlDB := stdlib.OpenDBFromPool(db)
	defer func() { _ = sqlDB.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.DownTo(ctx, 20260702085628); err != nil {
		t.Fatalf("goose down responsibility migration: %v", err)
	}

	userID := uuid.NewString()
	agentID := "agent-" + uuid.NewString()
	sessionID := "session-" + uuid.NewString()
	goalID := "goal-" + uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, 'failure-map@test.local')`, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, 'Failure Map Agent', '/tmp')`, agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active, created_at, updated_at)
		VALUES ($1, $2, 'migration', 'task', 'task', $3, $4, now(), now(), now())`, uuid.NewString(), sessionID, agentID, userID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO agent_goal (id, user_id, agent_id, root_id, session_id, title)
		VALUES ($1, $2, $3, $1, $4, 'migration goal')`, goalID, userID, agentID, sessionID); err != nil {
		t.Fatalf("seed goal: %v", err)
	}
	oldClasses := []string{"structural", "semantic", "transient"}
	for i, class := range oldClasses {
		if _, err := db.Exec(ctx, `
			INSERT INTO agent_goal_attempt (id, goal_id, user_id, agent_id, session_id, attempt_no, status, failure_class)
			VALUES ($1, $2, $3, $4, $5, $6, 'failed', $7)`, uuid.NewString(), goalID, userID, agentID, sessionID, i+1, class); err != nil {
			t.Fatalf("seed attempt %s: %v", class, err)
		}
	}

	if _, err := provider.UpTo(ctx, 20260702110624); err != nil {
		t.Fatalf("goose up responsibility migration: %v", err)
	}
	rows, err := db.Query(ctx, `SELECT previous_failure_class, failure_class FROM agent_goal_attempt ORDER BY attempt_no`)
	if err != nil {
		t.Fatalf("query mapped attempts: %v", err)
	}
	defer rows.Close()
	wantMapped := [][2]string{{"structural", "model"}, {"semantic", "model"}, {"transient", "flaky"}}
	for i := 0; rows.Next(); i++ {
		var prev, class string
		if err := rows.Scan(&prev, &class); err != nil {
			t.Fatalf("scan mapped attempt: %v", err)
		}
		if i >= len(wantMapped) || prev != wantMapped[i][0] || class != wantMapped[i][1] {
			t.Fatalf("mapped row %d=(%q,%q), want %v", i, prev, class, wantMapped)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("mapped rows: %v", err)
	}

	if _, err := provider.DownTo(ctx, 20260702085628); err != nil {
		t.Fatalf("goose down responsibility migration after map: %v", err)
	}
	if columnExists(t, db, "agent_goal_attempt", "previous_failure_class") {
		t.Fatal("previous_failure_class should not exist after down")
	}
	rows, err = db.Query(ctx, `SELECT failure_class FROM agent_goal_attempt ORDER BY attempt_no`)
	if err != nil {
		t.Fatalf("query restored attempts: %v", err)
	}
	defer rows.Close()
	for i := 0; rows.Next(); i++ {
		var class string
		if err := rows.Scan(&class); err != nil {
			t.Fatalf("scan restored attempt: %v", err)
		}
		if i >= len(oldClasses) || class != oldClasses[i] {
			t.Fatalf("restored row %d=%q, want %v", i, class, oldClasses)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("restored rows: %v", err)
	}
}

func TestFactsMigrationDownFlushesActiveIdentityFacts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	userID := uuid.NewString()

	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, 'facts-down@test.local')`, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, model, model_strong, model_fast, system_prompt, workspace, scope, creator_id, enabled)
		VALUES ('facts-down-agent', 'Facts Down Agent', '', '', '', '', '', 'system', '', true)`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ctx_agent_memory (user_id, agent_id, version) VALUES ($1, 'facts-down-agent', 7)`, userID); err != nil {
		t.Fatalf("seed memory row: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO facts (subject, scope, user_id, agent_id, content, status, metadata, source)
		VALUES
		  ('user', 'user_agent', $1, 'facts-down-agent', 'profile from active fact', 'active', '{}', 'manual'),
		  ('agent', 'user_agent', $1, 'facts-down-agent', 'soul from active fact', 'active', '{}', 'manual')`, userID); err != nil {
		t.Fatalf("seed identity facts: %v", err)
	}

	sub, err := fs.Sub(MigrationsFS, "migrations")
	if err != nil {
		t.Fatalf("open migrations fs: %v", err)
	}
	sqlDB := stdlib.OpenDBFromPool(db)
	defer func() { _ = sqlDB.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	// Roll back to just before the facts migration (20260625090000), so this
	// test exercises the facts Down regardless of any later migrations stacked
	// on top of it.
	if _, err := provider.DownTo(ctx, 20260622051501); err != nil {
		t.Fatalf("goose down to before facts migration: %v", err)
	}

	var content, soul string
	if err := db.QueryRow(ctx, `SELECT content, soul FROM ctx_agent_memory WHERE user_id = $1 AND agent_id = 'facts-down-agent'`, userID).Scan(&content, &soul); err != nil {
		t.Fatalf("read memory row after down: %v", err)
	}
	if content != "profile from active fact" {
		t.Fatalf("content after down = %q, want active profile fact", content)
	}
	if soul != "soul from active fact" {
		t.Fatalf("soul after down = %q, want active soul fact", soul)
	}
	if tableExists(t, db, "facts") {
		t.Fatal("facts table should be dropped after down")
	}
}

func TestReflectProvenanceBackfillMarksOnlyLegacyReflectFacts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	sub, err := fs.Sub(MigrationsFS, "migrations")
	if err != nil {
		t.Fatalf("open migrations fs: %v", err)
	}
	sqlDB := stdlib.OpenDBFromPool(db)
	defer func() { _ = sqlDB.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.DownTo(ctx, 20260707092307); err != nil {
		t.Fatalf("goose down to before reflect provenance backfill: %v", err)
	}

	userID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, 'reflect-backfill@test.local')`, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	agentReflect := "reflect-backfill-agent"
	agentManualLatest := "manual-latest-agent"
	agentAmbiguous := "ambiguous-agent"
	for _, agentID := range []string{agentReflect, agentManualLatest, agentAmbiguous} {
		if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, $1, '/tmp')`, agentID); err != nil {
			t.Fatalf("seed agent %s: %v", agentID, err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO ctx_agent_memory (user_id, agent_id, version) VALUES ($1, $2, 3)`, userID, agentID); err != nil {
			t.Fatalf("seed memory row %s: %v", agentID, err)
		}
	}

	reflectProfileFactID := uuid.NewString()
	reflectSoulFactID := uuid.NewString()
	manualLatestFactID := uuid.NewString()
	ambiguousFactID := uuid.NewString()
	seedMigratedFact := func(id, agentID, subject, content string) {
		t.Helper()
		if _, err := db.Exec(ctx, `
			INSERT INTO facts (id, subject, scope, user_id, agent_id, content, status, metadata, version, source, created_at, updated_at)
			VALUES ($1, $2, 'user_agent', $3, $4, $5, 'active', '{"migration":"20260625090000_add_facts_memory"}', 1, 'manual', now(), now())`,
			id, subject, userID, agentID, content); err != nil {
			t.Fatalf("seed fact %s: %v", id, err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO ctx_agent_memory_changelog (id, user_id, agent_id, entity_id, scope, action, source, memory_version_after, after_text, metadata)
			VALUES ($1, $2, $3, $4, 'fact', 'create', 'manual', 3, jsonb_build_object(
				'id', $4::text,
				'subject', $5::text,
				'scope', 'user_agent',
				'user_id', $2::uuid::text,
				'agent_id', $3::text,
				'content', $6::text,
				'status', 'active',
				'metadata', jsonb_build_object('migration', '20260625090000_add_facts_memory'),
				'version', 1,
				'source', 'manual',
				'created_at', now(),
				'updated_at', now()
			)::text, '{"migration":"20260625090000_add_facts_memory"}')`,
			uuid.NewString(), userID, agentID, id, subject, content); err != nil {
			t.Fatalf("seed fact changelog %s: %v", id, err)
		}
	}
	seedMigratedFact(reflectProfileFactID, agentReflect, "user", "reflect generated profile")
	seedMigratedFact(reflectSoulFactID, agentReflect, "agent", "reflect generated soul")
	seedMigratedFact(manualLatestFactID, agentManualLatest, "user", "manual latest profile")
	seedMigratedFact(ambiguousFactID, agentAmbiguous, "user", "ambiguous profile")

	seedLegacyIdentityChangelog := func(agentID, scope, source string, version int, after string) {
		t.Helper()
		if _, err := db.Exec(ctx, `
			INSERT INTO ctx_agent_memory_changelog (id, user_id, agent_id, scope, action, source, memory_version_after, after_text)
			VALUES ($1, $2, $3, $4, 'update', $5, $6, $7)`,
			uuid.NewString(), userID, agentID, scope, source, version, after); err != nil {
			t.Fatalf("seed legacy %s changelog for %s: %v", scope, agentID, err)
		}
	}
	seedLegacyIdentityChangelog(agentReflect, "profile", "manual", 1, "old manual profile")
	seedLegacyIdentityChangelog(agentReflect, "profile", "reflect", 2, "reflect generated profile")
	seedLegacyIdentityChangelog(agentReflect, "soul", "reflect", 2, "reflect generated soul")
	seedLegacyIdentityChangelog(agentManualLatest, "profile", "reflect", 1, "manual latest profile")
	seedLegacyIdentityChangelog(agentManualLatest, "profile", "manual", 2, "manual latest profile")

	if _, err := db.Exec(ctx, `
		INSERT INTO skill (id, scope, user_id, agent_id, name, description, status, metadata)
		VALUES ('ambiguous-skill', 'user_agent', $1, $2, 'ambiguous-skill', 'not proven reflect', 'active', '{"created-at":"2026-07-01T00:00:00Z"}')`,
		userID, agentReflect); err != nil {
		t.Fatalf("seed ambiguous skill: %v", err)
	}

	if _, err := provider.UpTo(ctx, 20260708090000); err != nil {
		t.Fatalf("goose up reflect provenance backfill: %v", err)
	}

	assertFactSource := func(id, want string) {
		t.Helper()
		var got string
		if err := db.QueryRow(ctx, `SELECT source FROM facts WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read fact source %s: %v", id, err)
		}
		if got != want {
			t.Fatalf("fact %s source = %q, want %q", id, got, want)
		}
	}
	assertFactSource(reflectProfileFactID, "reflect")
	assertFactSource(reflectSoulFactID, "reflect")
	assertFactSource(manualLatestFactID, "manual")
	assertFactSource(ambiguousFactID, "manual")

	var changelogSource, payloadSource string
	if err := db.QueryRow(ctx, `
		SELECT source, after_text::jsonb->>'source'
		FROM ctx_agent_memory_changelog
		WHERE scope = 'fact' AND entity_id = $1`,
		reflectProfileFactID).Scan(&changelogSource, &payloadSource); err != nil {
		t.Fatalf("read migrated fact changelog source: %v", err)
	}
	if changelogSource != "reflect" || payloadSource != "reflect" {
		t.Fatalf("migrated fact changelog source = %q payload=%q, want reflect/reflect", changelogSource, payloadSource)
	}

	var skillCreatedBy *string
	if err := db.QueryRow(ctx, `SELECT metadata->>'created_by' FROM skill WHERE id = 'ambiguous-skill'`).Scan(&skillCreatedBy); err != nil {
		t.Fatalf("read ambiguous skill metadata: %v", err)
	}
	if skillCreatedBy != nil {
		t.Fatalf("ambiguous skill created_by = %q, want null", *skillCreatedBy)
	}
}

func tableExists(t *testing.T, db *pgxpool.Pool, name string) bool {
	t.Helper()

	var exists bool
	if err := db.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatalf("check table %s exists: %v", name, err)
	}
	return exists
}

func columnExists(t *testing.T, db *pgxpool.Pool, table, column string) bool {
	t.Helper()

	var exists bool
	if err := db.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
		)`, table, column).Scan(&exists); err != nil {
		t.Fatalf("check column %s.%s exists: %v", table, column, err)
	}
	return exists
}

func TestSpanName(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"SELECT id, name FROM sessions WHERE id = ?", "SELECT sessions"},
		{"select * from ctx_messages", "SELECT ctx_messages"},
		{"INSERT INTO schema_migrations (version) VALUES (?)", "INSERT schema_migrations"},
		{"UPDATE settings_agents SET name = ? WHERE id = ?", "UPDATE settings_agents"},
		{"DELETE FROM sessions WHERE id = ?", "DELETE sessions"},
		{"PRAGMA foreign_keys = on", "PRAGMA"},
		{"BEGIN", "BEGIN"},
		{"", "query"},
		// sqlc keeps its "-- name:" annotation as the first line.
		{"-- name: GetActiveAutoAuthUserTokenByUser :one\nSELECT id, user_id FROM auth_user_token\nWHERE user_id = ?", "GetActiveAutoAuthUserTokenByUser (SELECT auth_user_token)"},
		{"-- name: CreateSession :exec\nINSERT INTO sessions (id) VALUES (?)", "CreateSession (INSERT sessions)"},
		{"-- name: PingDB :one\nPRAGMA foreign_keys", "PingDB (PRAGMA)"},
	}
	for _, c := range cases {
		if got := spanName(c.query); got != c.want {
			t.Errorf("spanName(%q) = %q, want %q", c.query, got, c.want)
		}
	}
}
