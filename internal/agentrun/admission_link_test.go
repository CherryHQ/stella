package agentrun_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestAdmissionLinkRollsBackRunAndDoesNotFollowNestedExecution(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	if _, err := db.Exec(ctx, `CREATE TABLE admission_link_probe (run_id uuid PRIMARY KEY REFERENCES agent_run(id))`); err != nil {
		t.Fatal(err)
	}
	store := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatal(err)
	}
	createSession := func() string {
		t.Helper()
		id := uuid.NewString()
		if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation(session_id) VALUES ($1)`, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	sessionID := createSession()
	rejected := errors.New("source claim expired")
	calls := 0
	linkCtx := agentrun.WithAdmissionLink(ctx, func(ctx context.Context, tx pgx.Tx, guard agentrun.Guard) error {
		calls++
		if guard.SessionID != sessionID {
			t.Fatalf("link inherited by a different session: %s", guard.SessionID)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO admission_link_probe(run_id) VALUES ($1)`, guard.RunID); err != nil {
			return err
		}
		return rejected
	})
	if _, err := store.Acquire(linkCtx, sessionID, "channel"); !errors.Is(err, rejected) {
		t.Fatalf("rejected admission = %v", err)
	}
	var runs, links int
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_run), (SELECT count(*) FROM admission_link_probe)`).Scan(&runs, &links); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || links != 0 {
		t.Fatalf("rollback left runs=%d links=%d", runs, links)
	}
	rejected = nil
	lease, err := store.Acquire(linkCtx, sessionID, "channel")
	if err != nil {
		t.Fatal(err)
	}
	var linkedRun string
	if err := db.QueryRow(ctx, `SELECT run_id::text FROM admission_link_probe`).Scan(&linkedRun); err != nil {
		t.Fatal(err)
	}
	if linkedRun != lease.Guard.RunID {
		t.Fatalf("source linked %s, admitted %s", linkedRun, lease.Guard.RunID)
	}
	for _, nestedCtx := range []context.Context{lease.Context(), lease.ContextWith(linkCtx)} {
		nested, err := store.Acquire(nestedCtx, createSession(), "delegate")
		if err != nil {
			t.Fatal(err)
		}
		if err := nested.Finish(ctx, agentrun.StatusCompleted, ""); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("link called %d times, want rejected and accepted admissions only", calls)
	}
	if err := lease.Finish(ctx, agentrun.StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
}
