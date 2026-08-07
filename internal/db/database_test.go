package db

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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

func TestChannelGroupAllowlistMigrationPreservesKnownGroups(t *testing.T) {
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
	if _, err := provider.DownTo(ctx, sequentialAnchor+5); err != nil {
		t.Fatalf("goose down allowlist migration: %v", err)
	}

	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ('allowlist-agent', 'Allowlist Agent', '/tmp')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO channel (id, name, type, config) VALUES
			('telegram-known', 'Telegram known', 'telegram', '{}'),
			('telegram-explicit', 'Telegram explicit', 'telegram', '{"allowed_chat_ids":""}'),
			('telegram-malformed', 'Telegram malformed', 'telegram', '{'),
			('feishu-known', 'Feishu known', 'feishu', '{"groups":{"oc_legacy":{}}}')`); err != nil {
		t.Fatalf("seed channels: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_state (id, platform, platform_group_id) VALUES
			('00000000-0000-0000-0000-000000000101', 'telegram', '-100'),
			('00000000-0000-0000-0000-000000000102', 'telegram', '-200'),
			('00000000-0000-0000-0000-000000000103', 'feishu', 'oc_member'),
			('00000000-0000-0000-0000-000000000104', 'telegram', '-300')`); err != nil {
		t.Fatalf("seed group states: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO channel_group_member (group_id, agent_id, reply_channel_id) VALUES
			('00000000-0000-0000-0000-000000000101', 'allowlist-agent', 'telegram-known'),
			('00000000-0000-0000-0000-000000000102', 'allowlist-agent', 'telegram-explicit'),
			('00000000-0000-0000-0000-000000000103', 'allowlist-agent', 'feishu-known'),
			('00000000-0000-0000-0000-000000000104', 'allowlist-agent', 'telegram-malformed')`); err != nil {
		t.Fatalf("seed group membership: %v", err)
	}

	if _, err := provider.UpTo(ctx, sequentialAnchor+6); err != nil {
		t.Fatalf("goose up allowlist migration: %v", err)
	}
	for _, tc := range []struct {
		channelID string
		want      string
	}{
		{channelID: "telegram-known", want: "-100"},
		{channelID: "telegram-explicit", want: ""},
		{channelID: "feishu-known", want: "oc_legacy,oc_member"},
	} {
		var got string
		if err := db.QueryRow(ctx, `SELECT config::jsonb->>'allowed_chat_ids' FROM channel WHERE id = $1`, tc.channelID).Scan(&got); err != nil {
			t.Fatalf("read %s allowlist: %v", tc.channelID, err)
		}
		if got != tc.want {
			t.Fatalf("%s allowlist = %q, want %q", tc.channelID, got, tc.want)
		}
	}
	var malformed string
	if err := db.QueryRow(ctx, `SELECT config FROM channel WHERE id = 'telegram-malformed'`).Scan(&malformed); err != nil {
		t.Fatalf("read malformed config: %v", err)
	}
	if malformed != "{" {
		t.Fatalf("malformed config = %q, want original value", malformed)
	}
}

func TestLibraryMigrationReplacesUnreleasedKnowledgeSchema(t *testing.T) {
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

	// The old Knowledge schema existed only in unreleased development commits.
	// The forward migration deliberately replaces it instead of
	// preserving rows under obsolete names.
	if _, err := provider.DownTo(ctx, 20260804120000); err != nil {
		t.Fatalf("goose down post-anchor migrations: %v", err)
	}
	if tableExists(t, db, "knowledge_file") ||
		tableExists(t, db, "knowledge_chunk_set") ||
		tableExists(t, db, "knowledge_chunk") ||
		tableExists(t, db, "library_file") ||
		tableExists(t, db, "library_chunk_set") ||
		tableExists(t, db, "library_chunk") {
		t.Fatal("Library tables should not exist before the post-anchor migrations")
	}
	if _, err := provider.UpTo(ctx, sequentialAnchor+1); err != nil {
		t.Fatalf("goose up unreleased Knowledge schema: %v", err)
	}
	if !tableExists(t, db, "knowledge_file") ||
		!tableExists(t, db, "knowledge_chunk_set") ||
		!tableExists(t, db, "knowledge_chunk") {
		t.Fatal("unreleased Knowledge tables should exist before replacement")
	}

	fileID := uuid.NewString()
	if _, err := db.Exec(ctx, `
		INSERT INTO knowledge_file (id, scope, file_name, media_type, size_bytes, raw_sha256)
		VALUES ($1, 'system', 'unreleased.txt', 'text/plain', 1, $2)
	`, fileID, make([]byte, 32)); err != nil {
		t.Fatalf("seed unreleased Knowledge row: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("goose up Library replacement migration: %v", err)
	}
	if tableExists(t, db, "knowledge_file") ||
		tableExists(t, db, "knowledge_chunk_set") ||
		tableExists(t, db, "knowledge_chunk") {
		t.Fatal("legacy Knowledge tables should not remain after replacement")
	}
	if !tableExists(t, db, "library_file") ||
		!tableExists(t, db, "library_chunk_set") ||
		!tableExists(t, db, "library_chunk") {
		t.Fatal("Library tables should exist after replacement")
	}
	var rows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM library_file WHERE id = $1`, fileID).Scan(&rows); err != nil {
		t.Fatalf("read replacement Library row: %v", err)
	}
	if rows != 0 {
		t.Fatalf("replacement Library rows = %d, want 0", rows)
	}

	var legacySchemaNames string
	if err := db.QueryRow(ctx, `
		SELECT coalesce(string_agg(name, ', ' ORDER BY name), '') FROM (
			SELECT conname AS name
			FROM pg_constraint
			WHERE connamespace = current_schema()::regnamespace
			  AND conname ~ '^knowledge_(file|chunk)'
			UNION ALL
			SELECT relname AS name
			FROM pg_class
			WHERE relnamespace = current_schema()::regnamespace
			  AND relkind = 'i'
			  AND relname ~ '^(knowledge_(file|chunk)|idx_knowledge_(file|chunk))'
		) AS legacy_names
	`).Scan(&legacySchemaNames); err != nil {
		t.Fatalf("inspect replacement Library schema objects: %v", err)
	}
	if legacySchemaNames != "" {
		t.Fatalf("legacy Knowledge schema object names remain: %s", legacySchemaNames)
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
	agentContentMismatch := "content-mismatch-agent"
	agentDeprecated := "deprecated-agent"
	for _, agentID := range []string{agentReflect, agentManualLatest, agentAmbiguous, agentContentMismatch, agentDeprecated} {
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
	contentMismatchFactID := uuid.NewString()
	deprecatedFactID := uuid.NewString()
	seedMigratedFact := func(id, agentID, subject, status, content string) {
		t.Helper()
		if _, err := db.Exec(ctx, `
			INSERT INTO facts (id, subject, scope, user_id, agent_id, content, status, metadata, version, source, created_at, updated_at)
			VALUES ($1, $2, 'user_agent', $3, $4, $5, $6, '{"migration":"20260625090000_add_facts_memory"}', 1, 'manual', now(), now())`,
			id, subject, userID, agentID, content, status); err != nil {
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
				'status', $7::text,
				'metadata', jsonb_build_object('migration', '20260625090000_add_facts_memory'),
				'version', 1,
				'source', 'manual',
				'created_at', now(),
				'updated_at', now()
			)::text, '{"migration":"20260625090000_add_facts_memory"}')`,
			uuid.NewString(), userID, agentID, id, subject, content, status); err != nil {
			t.Fatalf("seed fact changelog %s: %v", id, err)
		}
	}
	seedMigratedFact(reflectProfileFactID, agentReflect, "user", "active", "reflect generated profile")
	seedMigratedFact(reflectSoulFactID, agentReflect, "agent", "active", "reflect generated soul")
	seedMigratedFact(manualLatestFactID, agentManualLatest, "user", "active", "manual latest profile")
	seedMigratedFact(ambiguousFactID, agentAmbiguous, "user", "active", "ambiguous profile")
	seedMigratedFact(contentMismatchFactID, agentContentMismatch, "user", "active", "locally edited profile")
	seedMigratedFact(deprecatedFactID, agentDeprecated, "user", "deprecated", "deprecated reflect profile")

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
	seedLegacyIdentityChangelog(agentContentMismatch, "profile", "reflect", 1, "legacy reflect profile")
	seedLegacyIdentityChangelog(agentDeprecated, "profile", "reflect", 1, "deprecated reflect profile")

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
	assertFactSource(contentMismatchFactID, "manual")
	assertFactSource(deprecatedFactID, "manual")

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
}

func TestReflectUsageBackfillSeedsOnlyProvenReflectOwnedRows(t *testing.T) {
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
	if _, err := provider.DownTo(ctx, 20260708090000); err != nil {
		t.Fatalf("goose down to before reflect usage tracking: %v", err)
	}

	userID := uuid.NewString()
	agentID := "reflect-usage-backfill-agent"
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, 'reflect-usage-backfill@test.local')`, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, $1, '/tmp')`, agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	reflectFactID := uuid.NewString()
	manualFactID := uuid.NewString()
	profileFactID := uuid.NewString()
	if _, err := db.Exec(ctx, `
		INSERT INTO facts (id, subject, scope, user_id, agent_id, content, status, metadata, version, source)
		VALUES
		  ($1, 'world', 'user_agent', $4, $5, 'owned world fact', 'active', '{}', 1, 'reflect'),
		  ($2, 'world', 'user_agent', $4, $5, 'legacy/manual world fact', 'active', '{}', 1, 'manual'),
		  ($3, 'user', 'user_agent', $4, $5, 'reflect profile singleton', 'active', '{}', 1, 'reflect')`,
		reflectFactID, manualFactID, profileFactID, userID, agentID); err != nil {
		t.Fatalf("seed facts: %v", err)
	}

	if _, err := db.Exec(ctx, `
		INSERT INTO skill (id, scope, user_id, agent_id, name, description, status, metadata)
		VALUES
		  ('reflect-skill', 'user_agent', $1, $2, 'reflect-skill', 'owned reflect skill', 'active', '{"created_by":"reflect"}'),
		  ('legacy-skill', 'user_agent', $1, $2, 'legacy-skill', 'unmarked legacy skill', 'active', '{}'),
		  ('deprecated-reflect-skill', 'user_agent', $1, $2, 'deprecated-reflect-skill', 'old reflect skill', 'deprecated', '{"created_by":"reflect"}')`,
		userID, agentID); err != nil {
		t.Fatalf("seed skills: %v", err)
	}

	if _, err := provider.UpTo(ctx, 20260709090000); err != nil {
		t.Fatalf("goose up reflect usage tracking: %v", err)
	}

	assertKnowledgeUsage := func(factID string, want int) {
		t.Helper()
		var got int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM knowledge_usage WHERE fact_id = $1`, factID).Scan(&got); err != nil {
			t.Fatalf("count knowledge_usage %s: %v", factID, err)
		}
		if got != want {
			t.Fatalf("knowledge_usage rows for %s = %d, want %d", factID, got, want)
		}
	}
	assertKnowledgeUsage(reflectFactID, 1)
	assertKnowledgeUsage(manualFactID, 0)
	assertKnowledgeUsage(profileFactID, 0)

	assertSkillUsage := func(skillID string, wantRows int, wantUseCount int64) {
		t.Helper()
		var rows int
		var useCount int64
		if err := db.QueryRow(ctx, `SELECT count(*), COALESCE(max(use_count), 0) FROM skill_usage WHERE skill_id = $1`, skillID).Scan(&rows, &useCount); err != nil {
			t.Fatalf("count skill_usage %s: %v", skillID, err)
		}
		if rows != wantRows || useCount != wantUseCount {
			t.Fatalf("skill_usage for %s = (%d rows, use_count %d), want (%d rows, use_count %d)", skillID, rows, useCount, wantRows, wantUseCount)
		}
	}
	assertSkillUsage("reflect-skill", 1, 0)
	assertSkillUsage("legacy-skill", 0, 0)
	assertSkillUsage("deprecated-reflect-skill", 0, 0)

	assertIndexDefinition := func(name string, want string) {
		t.Helper()
		var got string
		if err := db.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1`, name).Scan(&got); err != nil {
			t.Fatalf("read index %s: %v", name, err)
		}
		if !strings.Contains(got, want) {
			t.Fatalf("index %s = %q, want it to contain %q", name, got, want)
		}
	}
	assertIndexDefinition("idx_knowledge_usage_last_used", "(user_id, agent_id, last_used_at, fact_id)")
	assertIndexDefinition("idx_skill_usage_last_used", "(user_id, agent_id, last_used_at, skill_id) INCLUDE (use_count)")
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

// TestCtxConversationGroupIDBackfillMigration seeds canonical group and private
// rows before the group_id migration, applies the real migration, and asserts the
// backfill populates only the canonical group conversation while leaving private
// and non-canonical rows NULL. It uses the repository's DownTo/seed/UpTo pattern.
func TestCtxConversationGroupIDBackfillMigration(t *testing.T) {
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

	// Roll back to just before the group_id migration.
	if _, err := provider.DownTo(ctx, 20260709120000); err != nil {
		t.Fatalf("goose down to before group_id migration: %v", err)
	}
	if columnExists(t, db, "ctx_conversation", "group_id") {
		t.Fatal("group_id should not exist before the migration")
	}

	const agentID = "agent-a"
	gid := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_group_state (id, platform, platform_group_id) VALUES ($1, 'test', $2)`, gid, "grp-"+gid); err != nil {
		t.Fatalf("seed group state: %v", err)
	}

	// Canonical group conversation: user_id == group uuid and the derived key.
	groupSessionID := agentID + ":group:" + gid
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id) VALUES ($1, $2, $3, 'chat', $4, $5)`,
		uuid.NewString(), groupSessionID, "group:"+gid, agentID, gid); err != nil {
		t.Fatalf("seed canonical group conversation: %v", err)
	}
	// Private conversation: real user, should never be backfilled.
	privateSessionID := agentID + ":user:user-1:private"
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id) VALUES ($1, $2, 'web', 'chat', $3, 'user-1')`,
		uuid.NewString(), privateSessionID, agentID); err != nil {
		t.Fatalf("seed private conversation: %v", err)
	}
	// Non-canonical row: user_id == group uuid but session_id does not match the
	// derived group key, so the predicate must leave it NULL.
	noncanonSessionID := "arbitrary-key"
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id) VALUES ($1, $2, 'web', 'chat', $3, $4)`,
		uuid.NewString(), noncanonSessionID, agentID, gid); err != nil {
		t.Fatalf("seed non-canonical conversation: %v", err)
	}

	// Apply the real migration (adds column, FK, CHECK, index, and backfills).
	if _, err := provider.UpTo(ctx, 20260711134243); err != nil {
		t.Fatalf("goose up group_id migration: %v", err)
	}

	groupIDOf := func(sessionID string) (string, bool) {
		t.Helper()
		var g pgtype.Text
		if err := db.QueryRow(ctx, `SELECT group_id::text FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&g); err != nil {
			t.Fatalf("read group_id for %s: %v", sessionID, err)
		}
		return g.String, g.Valid
	}

	if got, ok := groupIDOf(groupSessionID); !ok || got != gid {
		t.Fatalf("canonical group conversation group_id = (%q, valid=%v), want %q", got, ok, gid)
	}
	if _, ok := groupIDOf(privateSessionID); ok {
		t.Fatal("private conversation group_id must stay NULL")
	}
	if _, ok := groupIDOf(noncanonSessionID); ok {
		t.Fatal("non-canonical conversation group_id must stay NULL")
	}
}
