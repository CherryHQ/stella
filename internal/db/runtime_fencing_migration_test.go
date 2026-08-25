package db

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestRuntimeFencingMigrationBackfillsConsumedNewCommands(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := reflectWatermarkProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	if _, err := provider.DownTo(ctx, sequentialAnchor+24); err != nil {
		t.Fatalf("restore pre-runtime-fencing schema: %v", err)
	}
	const channelID = "runtime-fencing-migration-channel"
	consumedAt := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(ctx, `
		INSERT INTO channel (id, name, type, enabled, config, created_at, updated_at)
		VALUES ($1, 'Runtime fencing migration', 'discord', true, '{}'::jsonb, $2, $2)
	`, channelID, consumedAt); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO channel_chat_command_receipt (
			channel_id, chat_key, message_id, command, binding, created_at, updated_at
		) VALUES ($1, 'physical-chat', 'consumed-new-message', '/new', 'durable-binding', $2, $2)
	`, channelID, consumedAt); err != nil {
		t.Fatalf("seed consumed /new receipt: %v", err)
	}

	if _, err := provider.UpTo(ctx, sequentialAnchor+25); err != nil {
		t.Fatalf("apply runtime-fencing migration: %v", err)
	}
	var status, kind string
	var expectedSession pgtype.Text
	var createdAt time.Time
	if err := db.QueryRow(ctx, `
		SELECT status, kind, expected_session_id, created_at
		FROM channel_binding_fifo
		WHERE channel_id = $1
		  AND source_key = 'command:physical-chat:consumed-new-message'
	`, channelID).Scan(&status, &kind, &expectedSession, &createdAt); err != nil {
		t.Fatalf("read backfilled historical /new receipt: %v", err)
	}
	if status != "completed" || kind != "new" || expectedSession.Valid || !createdAt.Equal(consumedAt) {
		t.Fatalf("historical /new FIFO = status %q kind %q expected %#v created %s, want completed/new/NULL/%s", status, kind, expectedSession, createdAt, consumedAt)
	}
}
