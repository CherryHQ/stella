package db

import (
	"io/fs"
	"os"
	"testing"
	"testing/fstest"

	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const (
	pluginCutoverMigration40 = int64(90000000000040)
	pluginCutoverMigration41 = int64(90000000000041)
)

// TestPluginCutoverCandidateThroughGoose exercises the external candidate as
// Goose would apply it. Set STELLA_FINAL_MIGRATION_CUTOVER_SQL to the reviewed
// candidate path while it remains outside the embedded migration set; remove
// this indirection once migration 41 is finalized in the repository.
func TestPluginCutoverCandidateThroughGoose(t *testing.T) {
	candidatePath := os.Getenv("STELLA_FINAL_MIGRATION_CUTOVER_SQL")
	if candidatePath == "" {
		t.Skip("set STELLA_FINAL_MIGRATION_CUTOVER_SQL to run the external migration candidate")
	}
	candidate, err := os.ReadFile(candidatePath)
	if err != nil {
		t.Fatal(err)
	}

	db := newTestDB(t)
	ctx := t.Context()
	sqlDB := stdlib.OpenDBFromPool(db)
	t.Cleanup(func() { _ = sqlDB.Close() })
	baseMigrations, err := fs.Sub(MigrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	baseProvider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, baseMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := baseProvider.DownTo(ctx, pluginCutoverMigration40); err != nil {
		t.Fatalf("down to preparation migration 40: %v", err)
	}
	candidateProvider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, candidateMigrationFS(t, candidate))
	if err != nil {
		t.Fatal(err)
	}
	user := insertPluginUser(t, db, "plugin-cutover-candidate@example.test", false)
	const registrationID = "0198f9a4-1b2c-7def-8123-456789abcdef"
	if _, err := db.Exec(ctx, `
		INSERT INTO mcp_server (id, scope, name, url, transport, auth_type,
			enabled, metadata, tools, status, credential_mode)
		VALUES ($1, 'system', 'candidate-oauth', 'https://mcp.example.test', 'sse', 'oauth',
			true, '{"oauth":{"client_id":"candidate-client"}}'::jsonb,
			'[]'::jsonb, 'needs_auth', 'shared')
	`, registrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO mcp_oauth_flow (
			server_id, user_id, credential_scope, pkce_verifier, oauth_config, expires_at
		) VALUES ($1, $2, 'system', 'candidate-verifier', '{"client_id":"candidate-client"}'::jsonb,
			now() + interval '5 minutes')
	`, registrationID, user.UserID()); err != nil {
		t.Fatal(err)
	}

	if _, err := candidateProvider.UpTo(ctx, pluginCutoverMigration41); err != nil {
		t.Fatalf("apply candidate migration 41: %v", err)
	}
	if err := plugin.ImportLegacyState(ctx, db, plugin.NewCatalog(), nil); err != nil {
		t.Fatalf("import legacy state after candidate migration: %v", err)
	}

	var configID string
	if err := db.QueryRow(ctx, `
		SELECT id::text FROM plugin_config WHERE id = $1::uuid AND plugin_id = $2
	`, registrationID, "custom/"+registrationID).Scan(&configID); err != nil {
		t.Fatal(err)
	}
	if configID != registrationID {
		t.Fatalf("UUID-preserving config id = %q, want %q", configID, registrationID)
	}
	var flowConfigID string
	if err := db.QueryRow(ctx, `SELECT server_id::text FROM mcp_oauth_flow WHERE server_id = $1::uuid`, registrationID).Scan(&flowConfigID); err != nil {
		t.Fatal(err)
	}
	if flowConfigID != registrationID {
		t.Fatalf("OAuth flow server id = %q, want %q", flowConfigID, registrationID)
	}
	var marker string
	if err := db.QueryRow(ctx, `SELECT value FROM app_setting WHERE key = 'plugin_cutover_v1'`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != "v1" {
		t.Fatalf("cutover marker = %q", marker)
	}

	if _, err := candidateProvider.DownTo(ctx, pluginCutoverMigration40); err == nil {
		t.Fatal("candidate Down unexpectedly succeeded")
	}
	var latestVersion int64
	var applied bool
	if err := db.QueryRow(ctx, `
		SELECT version_id, is_applied FROM goose_db_version ORDER BY id DESC LIMIT 1
	`).Scan(&latestVersion, &applied); err != nil {
		t.Fatal(err)
	}
	if latestVersion != pluginCutoverMigration41 || !applied {
		t.Fatalf("latest migration ledger = %d applied=%v, want 41/applied", latestVersion, applied)
	}
	var stateTableExists bool
	if err := db.QueryRow(ctx, `SELECT to_regclass('public.mcp_connection_state') IS NOT NULL`).Scan(&stateTableExists); err != nil {
		t.Fatal(err)
	}
	if !stateTableExists {
		t.Fatal("candidate Down removed mcp_connection_state")
	}
}

func candidateMigrationFS(t *testing.T, candidate []byte) fs.FS {
	t.Helper()
	files := make(fstest.MapFS)
	if err := fs.WalkDir(MigrationsFS, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(MigrationsFS, name)
		if err != nil {
			return err
		}
		files[name] = &fstest.MapFile{Data: data, Mode: 0o644}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	files["migrations/90000000000041_plugin_runtime_identity.sql"] = &fstest.MapFile{Data: candidate, Mode: 0o644}
	migrations, err := fs.Sub(files, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	return migrations
}
