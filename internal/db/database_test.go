package db

import (
	"context"
	"database/sql"
	"testing"
)

func TestOpenDBFreshInstallDoesNotCreateFeishuTokensTable(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)

	if tableExists(t, db, "feishu_tokens") {
		t.Fatal("feishu_tokens table should not exist after fresh install migrations")
	}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	var exists bool
	if err := db.QueryRow("SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
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
		{"", "sql.conn.query"},
		// sqlc keeps its "-- name:" annotation as the first line.
		{"-- name: GetActiveAutoAuthUserTokenByUser :one\nSELECT id, user_id FROM auth_user_token\nWHERE user_id = ?", "GetActiveAutoAuthUserTokenByUser (SELECT auth_user_token)"},
		{"-- name: CreateSession :exec\nINSERT INTO sessions (id) VALUES (?)", "CreateSession (INSERT sessions)"},
		{"-- name: PingDB :one\nPRAGMA foreign_keys", "PingDB (PRAGMA)"},
	}
	for _, c := range cases {
		if got := spanName(context.Background(), "sql.conn.query", c.query); got != c.want {
			t.Errorf("spanName(%q) = %q, want %q", c.query, got, c.want)
		}
	}
}
