package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const (
	groupMemoryCutoverStart = "-- stella-group-memory-cutover-begin"
	groupMemoryCutoverEnd   = "-- stella-group-memory-cutover-end"
)

func TestGroupMemoryCutoverRunbookStartsAtCurrentHead(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	groupID := uuid.NewString()

	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_state (
			id, platform, platform_group_id, next_seq
		)
		VALUES ($1, 'test', 'cutover-group', 4)
	`, groupID); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_message (
			id, group_id, seq, actor_type, actor_id, content
		)
		VALUES
			($1, $3, 1, 'human', 'alice', 'old one'),
			($2, $3, 4, 'agent', 'agent-1', 'old four')
	`, uuid.NewString(), uuid.NewString(), groupID); err != nil {
		t.Fatalf("seed legacy events: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_memory (group_id, content, version)
		VALUES ($1, 'legacy blob', 7)
	`, groupID); err != nil {
		t.Fatalf("seed legacy group memory: %v", err)
	}

	cutoverSQL := loadGroupMemoryCutoverSQL(t)
	if _, err := db.Exec(ctx, cutoverSQL); err != nil {
		t.Fatalf("execute cutover runbook SQL: %v", err)
	}

	var cursor int64
	if err := db.QueryRow(ctx, `
		SELECT last_seq
		FROM ctx_group_ingest_cursor
		WHERE group_id = $1 AND pipeline = 'group_reflect'
	`, groupID).Scan(&cursor); err != nil {
		t.Fatalf("read cutover cursor: %v", err)
	}
	if cursor != 4 {
		t.Fatalf("cutover cursor = %d, want current head 4", cursor)
	}
	var memoryRows, eventRows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_group_memory WHERE group_id = $1`, groupID).Scan(&memoryRows); err != nil {
		t.Fatalf("count legacy memory: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_group_message WHERE group_id = $1`, groupID).Scan(&eventRows); err != nil {
		t.Fatalf("count legacy events: %v", err)
	}
	if memoryRows != 0 || eventRows != 2 {
		t.Fatalf("post-cutover legacy memory/events = %d/%d, want 0/2", memoryRows, eventRows)
	}

	// Repeating the pre-activation transaction is safe and never rewinds a cursor.
	if _, err := db.Exec(ctx, cutoverSQL); err != nil {
		t.Fatalf("repeat cutover runbook SQL: %v", err)
	}
	if err := db.QueryRow(ctx, `
		SELECT last_seq
		FROM ctx_group_ingest_cursor
		WHERE group_id = $1 AND pipeline = 'group_reflect'
	`, groupID).Scan(&cursor); err != nil {
		t.Fatalf("read repeated cutover cursor: %v", err)
	}
	if cursor != 4 {
		t.Fatalf("repeated cutover cursor = %d, want 4", cursor)
	}

	if _, err := db.Exec(ctx, `UPDATE ctx_group_state SET next_seq = 5 WHERE id = $1`, groupID); err != nil {
		t.Fatalf("advance post-cutover group head: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_message (
			id, group_id, seq, actor_type, actor_id, content
		)
		VALUES ($1, $2, 5, 'human', 'alice', 'new after cutover')
	`, uuid.NewString(), groupID); err != nil {
		t.Fatalf("append post-cutover event: %v", err)
	}
	var pending int
	if err := db.QueryRow(ctx, `
		SELECT count(*)
		FROM ctx_group_message
		WHERE group_id = $1 AND seq > $2
	`, groupID, cursor).Scan(&pending); err != nil {
		t.Fatalf("count pending post-cutover events: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending post-cutover events = %d, want 1", pending)
	}
}

func TestGroupMemoryCutoverRunbookRejectsExistingStructuredFacts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	groupID := uuid.NewString()
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_state (id, platform, platform_group_id)
		VALUES ($1, 'test', 'already-structured')
	`, groupID); err != nil {
		t.Fatalf("seed structured group: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_fact (
			id, group_id, subject, content, status, source
		)
		VALUES ($1, $2, 'group', 'durable rule', 'active', 'reflect')
	`, uuid.NewString(), groupID); err != nil {
		t.Fatalf("seed structured fact: %v", err)
	}

	if _, err := db.Exec(ctx, loadGroupMemoryCutoverSQL(t)); err == nil {
		t.Fatal("cutover accepted a database that already contains structured facts")
	}
}

func loadGroupMemoryCutoverSQL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../docs/changes/group-chat-memory/cutover.md")
	if err != nil {
		t.Fatalf("read cutover runbook: %v", err)
	}
	text := string(raw)
	start := strings.Index(text, groupMemoryCutoverStart)
	end := strings.Index(text, groupMemoryCutoverEnd)
	if start < 0 || end <= start {
		t.Fatal("cutover runbook SQL markers are missing or out of order")
	}
	return text[start : end+len(groupMemoryCutoverEnd)]
}
