package run

import (
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/sessionexecution"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestQueuedRunCannotBypassLockedPredecessor(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	first := enqueueForTest(t, db, "sess-1", "first")
	second := enqueueForTest(t, db, "sess-1", "second")
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), "SELECT id FROM agent_run WHERE id=$1 FOR UPDATE", first); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, "second-worker", &fakeExecutor{reply: "second answer"}, replyHook(t))
	claimed, err := w.ProcessOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(db).Get(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("predecessor locked and queued, second claimed=%v state=%s", claimed, r.State)
	if claimed || r.State != "queued" {
		t.Fatal("later run bypassed its nonterminal predecessor")
	}
}

func TestArchivedQueuedRunDoesNotWedgeWorker(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "archived", "agent-1")
	createSession(t, db, "healthy", "agent-1")
	first := enqueueForTest(t, db, "archived", "first")
	enqueueForTest(t, db, "healthy", "healthy-one")
	if _, err := db.Exec(t.Context(), "UPDATE ctx_conversation SET archived=true WHERE session_id='archived'"); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, "worker", &fakeExecutor{reply: "reply"}, replyHook(t))
	for range 2 {
		claimed, err := w.ProcessOnce(t.Context())
		t.Logf("claimed=%v err=%v", claimed, err)
	}
	r, err := New(db).Get(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := db.QueryRow(t.Context(), "SELECT count(*) FROM agent_run WHERE state='queued'").Scan(&queued); err != nil {
		t.Fatal(err)
	}
	t.Logf("archived_state=%s remaining_queued=%d", r.State, queued)
	if r.State == "queued" {
		t.Fatal("archived target must become terminal so the worker can make progress")
	}
}

func TestExistingReaperDoesNotOrphanDurableRun(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	id := enqueueForTest(t, db, "sess-1", "one")
	w := NewWorker(db, "worker", &fakeExecutor{}, nil)
	_, _, lease, err := w.claim(t.Context(), &runOutcome{})
	if err != nil || lease == nil {
		t.Fatalf("claim: %v", err)
	}
	defer func() { _ = lease.Finish("error") }()
	if _, err := db.Exec(t.Context(), "UPDATE ctx_session_execution SET lease_until=clock_timestamp()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if err := sessionexecution.New(db).Reap(t.Context()); err != nil {
		t.Fatal(err)
	}
	n, err := New(db).ReapExpired(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r, err := sqlc.New(db).GetAgentRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("durable_reaped=%d run_state=%s", n, r.State)
	if r.State != "interrupted" {
		t.Fatal("generic reaper deleted the only lease link and left a running run")
	}
}
