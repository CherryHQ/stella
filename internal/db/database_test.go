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
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("goose down facts migration: %v", err)
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

func tableExists(t *testing.T, db *pgxpool.Pool, name string) bool {
	t.Helper()

	var exists bool
	if err := db.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatalf("check table %s exists: %v", name, err)
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
