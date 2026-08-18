package db

import (
	"context"
	"testing"
	"time"
)

const (
	groupOptimisticCutoverBeforeMigration = 90000000000018
	groupOptimisticCutoverMigration       = 90000000000020
)

func TestCutoverConvertsPendingReplyRowsAndLiveLeasesResume(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := reflectWatermarkProvider(t, db)
	defer closeProvider()
	ctx := context.Background()
	if _, err := provider.DownTo(ctx, groupOptimisticCutoverBeforeMigration); err != nil {
		t.Fatalf("restore pre-cutover schema: %v", err)
	}
	if _, err := provider.UpTo(ctx, groupOptimisticCutoverMigration-1); err != nil {
		t.Fatalf("restore fencing schema before cutover: %v", err)
	}

	const groupID = "11111111-1111-1111-1111-111111111111"
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace, sandbox) VALUES ('agent-1', 'Agent One', '/tmp', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO channel (id, name, type, enabled, config) VALUES ('ch-1', 'Channel One', 'web', true, '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ctx_group_state (id, platform, platform_group_id, platform_thread_id) VALUES ($1, 'web', 'cutover', '')`, groupID); err != nil {
		t.Fatal(err)
	}
	for seq, id := range map[int]string{
		1: "a1a1a1a1-0000-0000-0000-000000000001",
		2: "a1a1a1a1-0000-0000-0000-000000000002",
		3: "a1a1a1a1-0000-0000-0000-000000000003",
	} {
		if _, err := db.Exec(ctx, `INSERT INTO ctx_group_message (id, group_id, seq, actor_type, actor_id, content) VALUES ($1, $2, $3, 'human', 'user-1', 'hello')`, id, groupID, seq); err != nil {
			t.Fatal(err)
		}
	}
	liveLease := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Microsecond)
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_dispatch (id, group_message_id, group_id, agent_id, reply_channel_id, status, trigger_seq)
		VALUES ('d15a0000-0000-0000-0000-000000000001', 'a1a1a1a1-0000-0000-0000-000000000001', $1, 'agent-1', 'ch-1', 'pending', 1)
	`, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_dispatch (id, group_message_id, group_id, agent_id, reply_channel_id, status, attempt_count, lease_until, trigger_seq)
		VALUES ('d15a0000-0000-0000-0000-000000000002', 'a1a1a1a1-0000-0000-0000-000000000002', $1, 'agent-1', 'ch-1', 'running', 2, $2, 2)
	`, groupID, liveLease); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, groupOptimisticCutoverMigration); err != nil {
		t.Fatalf("run optimistic cutover: %v", err)
	}

	var pendingKind, runningKind, runningStatus string
	var resumedLease time.Time
	if err := db.QueryRow(ctx, `SELECT kind FROM ctx_group_dispatch WHERE id='d15a0000-0000-0000-0000-000000000001'`).Scan(&pendingKind); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT kind, status, lease_until FROM ctx_group_dispatch WHERE id='d15a0000-0000-0000-0000-000000000002'`).Scan(&runningKind, &runningStatus, &resumedLease); err != nil {
		t.Fatal(err)
	}
	if pendingKind != "wake" || runningKind != "wake" {
		t.Fatalf("cutover kinds pending/running=%q/%q, want wake/wake", pendingKind, runningKind)
	}
	if runningStatus != "running" || !resumedLease.Equal(liveLease) {
		t.Fatalf("live lease status/until=%q/%s, want running/%s", runningStatus, resumedLease, liveLease)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_dispatch (id, group_message_id, group_id, agent_id, reply_channel_id, status, trigger_seq)
		VALUES ('d15a0000-0000-0000-0000-000000000003', 'a1a1a1a1-0000-0000-0000-000000000003', $1, 'agent-1', 'ch-1', 'pending', 3)
	`, groupID); err != nil {
		t.Fatal(err)
	}
	var defaultKind string
	if err := db.QueryRow(ctx, `SELECT kind FROM ctx_group_dispatch WHERE id='d15a0000-0000-0000-0000-000000000003'`).Scan(&defaultKind); err != nil {
		t.Fatal(err)
	}
	if defaultKind != "wake" {
		t.Fatalf("post-cutover default kind=%q, want wake", defaultKind)
	}
}
