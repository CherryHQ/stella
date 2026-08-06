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

const (
	groupMemoryBeforeMigration = 90000000000003
	groupMemoryMigration       = 90000000000006
)

func TestGroupMemoryMigrationLeavesLegacyRowsUnbackfilled(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := groupMemoryMigrationProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	if _, err := provider.DownTo(ctx, groupMemoryBeforeMigration); err != nil {
		t.Fatalf("restore pre-group-memory schema: %v", err)
	}

	groupID := uuid.NewString()
	groupMessageID := uuid.NewString()
	conversationID := uuid.NewString()
	messageID := uuid.NewString()
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_state (id, platform, platform_group_id)
		VALUES ($1, 'test', 'legacy-group')
	`, groupID); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_message (id, group_id, seq, actor_type, actor_id, content)
		VALUES ($1, $2, 1, 'human', 'legacy-user', 'legacy public message')
	`, groupMessageID, groupID); err != nil {
		t.Fatalf("seed group message: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, user_id, agent_id, group_id)
		VALUES ($1, 'legacy-session', $2, 'legacy-agent', $3)
	`, conversationID, groupID, groupID); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_message (id, conversation_id, seq, role, content, token_count)
		VALUES ($1, $2, 1, 'user', 'legacy lcm message', 4)
	`, messageID, conversationID); err != nil {
		t.Fatalf("seed lcm message: %v", err)
	}

	if _, err := provider.UpTo(ctx, groupMemoryMigration); err != nil {
		t.Fatalf("apply group memory migration: %v", err)
	}

	var displayName, origin any
	if err := db.QueryRow(ctx, `
		SELECT actor_display_name
		FROM ctx_group_message
		WHERE id = $1
	`, groupMessageID).Scan(&displayName); err != nil {
		t.Fatalf("read legacy display name: %v", err)
	}
	if displayName != nil {
		t.Fatalf("legacy actor_display_name = %#v, want NULL", displayName)
	}
	if err := db.QueryRow(ctx, `
		SELECT origin_group_message_id
		FROM ctx_message
		WHERE id = $1
	`, messageID).Scan(&origin); err != nil {
		t.Fatalf("read legacy origin: %v", err)
	}
	if origin != nil {
		t.Fatalf("legacy origin_group_message_id = %#v, want NULL", origin)
	}
	if !tableExists(t, db, "ctx_group_fact") || !tableExists(t, db, "ctx_group_fact_changelog") {
		t.Fatal("group fact tables were not created")
	}
}

func TestGroupMemoryMigrationEnforcesOriginAndSubjectShape(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	var originFKValidated bool
	if err := db.QueryRow(ctx, `
		SELECT convalidated
		FROM pg_constraint
		WHERE conname = 'ctx_message_origin_group_message_id_fkey'
	`).Scan(&originFKValidated); err != nil {
		t.Fatalf("read origin foreign-key validation state: %v", err)
	}
	if !originFKValidated {
		t.Fatal("origin foreign key remains NOT VALID after all group memory migrations")
	}

	groupID := uuid.NewString()
	groupMessageID := uuid.NewString()
	conversationID := uuid.NewString()
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_state (id, platform, platform_group_id)
		VALUES ($1, 'test', 'new-group')
	`, groupID); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_message (
			id, group_id, seq, actor_type, actor_id, actor_display_name, content
		)
		VALUES ($1, $2, 1, 'human', 'user-1', 'Alice', 'public message')
	`, groupMessageID, groupID); err != nil {
		t.Fatalf("seed group message: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, user_id, agent_id, group_id)
		VALUES ($1, 'new-session', $2, 'agent-1', $3)
	`, conversationID, groupID, groupID); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_message (
			id, conversation_id, seq, role, content, token_count, origin_group_message_id
		)
		VALUES ($1, $2, 1, 'user', 'first copy', 3, $3)
	`, uuid.NewString(), conversationID, groupMessageID); err != nil {
		t.Fatalf("insert origin message: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_message (
			id, conversation_id, seq, role, content, token_count, origin_group_message_id
		)
		VALUES ($1, $2, 2, 'user', 'duplicate copy', 3, $3)
	`, uuid.NewString(), conversationID, groupMessageID); err == nil {
		t.Fatal("duplicate group origin was accepted in one conversation")
	}

	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_fact (
			id, group_id, subject, subject_id, content, status, source
		)
		VALUES ($1, $2, 'group', 'unexpected', 'invalid subject shape', 'active', 'reflect')
	`, uuid.NewString(), groupID); err == nil {
		t.Fatal("group subject with subject_id was accepted")
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_fact (
			id, group_id, subject, subject_id, content, status, source
		)
		VALUES ($1, $2, 'human', NULL, 'invalid subject shape', 'active', 'reflect')
	`, uuid.NewString(), groupID); err == nil {
		t.Fatal("human subject without subject_id was accepted")
	}
}

func groupMemoryMigrationProvider(t *testing.T, pool *pgxpool.Pool) (*goose.Provider, func()) {
	t.Helper()
	migrations, err := fs.Sub(MigrationsFS, "migrations")
	if err != nil {
		t.Fatalf("open migrations: %v", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("create migration provider: %v", err)
	}
	return provider, func() { _ = sqlDB.Close() }
}
