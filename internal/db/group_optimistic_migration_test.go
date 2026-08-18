package db

import (
	"context"
	"testing"
	"time"
)

const (
	groupOptimisticBeforeMigration = 90000000000018
	groupOptimisticMigration       = 90000000000019
)

// Pre-migration dispatch rows carry arbiter routing decisions. The migration
// must hand them to triage as wake rows without stranding a live lease.
func TestOptimisticMigrationAdoptsExistingDispatchAndResumesLeases(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := reflectWatermarkProvider(t, db)
	defer closeProvider()
	ctx := context.Background()
	if _, err := provider.DownTo(ctx, groupOptimisticBeforeMigration); err != nil {
		t.Fatalf("restore pre-optimistic schema: %v", err)
	}

	const groupID = "11111111-1111-1111-1111-111111111111"
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace, sandbox) VALUES ('agent-1', 'Agent One', '/tmp', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO channel (id, name, type, enabled, config) VALUES ('ch-1', 'Channel One', 'web', true, '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ctx_group_state (id, platform, platform_group_id, platform_thread_id) VALUES ($1, 'web', 'optimistic', '')`, groupID); err != nil {
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
		INSERT INTO ctx_group_dispatch (id, group_message_id, group_id, agent_id, reply_channel_id, status)
		VALUES ('d15a0000-0000-0000-0000-000000000001', 'a1a1a1a1-0000-0000-0000-000000000001', $1, 'agent-1', 'ch-1', 'pending')
	`, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_group_dispatch (id, group_message_id, group_id, agent_id, reply_channel_id, status, attempt_count, lease_until)
		VALUES ('d15a0000-0000-0000-0000-000000000002', 'a1a1a1a1-0000-0000-0000-000000000002', $1, 'agent-1', 'ch-1', 'running', 2, $2)
	`, groupID, liveLease); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, groupOptimisticMigration); err != nil {
		t.Fatalf("run optimistic migration: %v", err)
	}

	var pendingKind, runningKind, runningStatus string
	var pendingTriggerSeq, runningTriggerSeq int64
	var resumedLease time.Time
	if err := db.QueryRow(ctx, `SELECT kind, trigger_seq FROM ctx_group_dispatch WHERE id='d15a0000-0000-0000-0000-000000000001'`).Scan(&pendingKind, &pendingTriggerSeq); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT kind, status, lease_until, trigger_seq FROM ctx_group_dispatch WHERE id='d15a0000-0000-0000-0000-000000000002'`).Scan(&runningKind, &runningStatus, &resumedLease, &runningTriggerSeq); err != nil {
		t.Fatal(err)
	}
	if pendingKind != "wake" || runningKind != "wake" {
		t.Fatalf("kinds pending/running=%q/%q, want wake/wake", pendingKind, runningKind)
	}
	if pendingTriggerSeq != 1 || runningTriggerSeq != 2 {
		t.Fatalf("backfilled trigger_seq pending/running=%d/%d, want 1/2", pendingTriggerSeq, runningTriggerSeq)
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
		t.Fatalf("post-migration default kind=%q, want wake", defaultKind)
	}
}
