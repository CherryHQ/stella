package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agent/run"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/channel/outbox"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/sessionexecution"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Exercise the real Runtime admission/forwarder under the worker's adopted
// lease. Only the model runner and memory provider are in-process test doubles.
type reviewRuntimeExecutor struct {
	rt   *Runtime
	info session.Info
}

func (e reviewRuntimeExecutor) Execute(ctx context.Context, r sqlc.AgentRun) (string, error) {
	var reply strings.Builder
	var result error
	for event := range e.rt.Chat(ctx, e.info, "hello", WithRunID(r.ID)) {
		reply.WriteString(event.Text)
		result = errors.Join(result, event.Err)
	}
	return reply.String(), result
}

func TestWorkerRealRuntimeFinalReply(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	q := sqlc.New(db)
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "review-agent", Name: "review", Scope: "system", Enabled: true, Workspace: t.TempDir(), Sandbox: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{ID: uuid.Must(uuid.NewV7()).String(), SessionID: "review-session", Kind: "chat", AgentID: pgtype.Text{String: "review-agent", Valid: true}, LastActive: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	info := session.Info{ID: "review-session", AgentID: "review-agent", UserID: "review-user", Kind: "chat"}
	rt, err := New(Config{Memory: &recordingMemory{}, Execution: sessionexecution.New(db), NewRunner: func(context.Context, RunnerParams) (Runner, error) {
		return &chatFakeRunner{events: []Event{{Text: "final reply"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	r, _, err := run.New(db).Enqueue(ctx, tx, run.EnqueueParams{SessionID: info.ID, AgentID: info.AgentID, RequestKey: "review-request", Actor: run.Actor{V: 1, Kind: "user", UserID: info.UserID}, Input: run.Input{V: 1, Text: "hello"}, ReplyAddress: run.ReplyAddress{V: 1, ChannelID: "ch-review", AccountKey: "bot", ChatKey: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	hookReply := "never-called"
	w := run.NewWorker(db, "review-worker", reviewRuntimeExecutor{rt: rt, info: info}, func(ctx context.Context, tx pgx.Tx, r sqlc.AgentRun, result, reply string) error {
		hookReply = reply
		if result != "success" || reply == "" {
			return nil
		}
		ops, err := outbox.ReplyOps(r.ID, outbox.DeliveryKeyForRun(r.ID), "ch-review", "bot", outbox.Address{V: 1, ChatKey: "chat"}, reply, 4000)
		if err != nil {
			return err
		}
		return outbox.New(db).Append(ctx, tx, ops)
	})
	claimed, processErr := w.ProcessOnce(ctx)
	got, err := run.New(db).Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var outboxCount int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM channel_outbox WHERE run_id=$1", r.ID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	t.Logf("claimed=%v run_state=%s hook_reply=%q outbox_count=%d process_error=%v", claimed, got.State, hookReply, outboxCount, processErr)
	if processErr != nil || got.State != "completed" || hookReply != "final reply" || outboxCount != 1 {
		t.Fatal("real Runtime must finish once with its final reply in the outbox")
	}
}

func TestChildChatMustNotFinishParentExecution(t *testing.T) {
	db := dbtest.New(t)
	parent, child := executionFixture(t, db), executionFixture(t, db)
	executions := sessionexecution.New(db)
	parentCtx, lease, err := executions.Claim(t.Context(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Finish("error") }()
	rt, err := New(Config{Memory: &recordingMemory{}, Execution: executions, NewRunner: func(context.Context, RunnerParams) (Runner, error) {
		return &chatFakeRunner{events: []Event{{Text: "child reply"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	for event := range rt.Chat(parentCtx, child, "hello child") {
		if event.Err != nil {
			t.Errorf("child error: %v", event.Err)
		}
	}
	_, parentErr := sqlc.New(db).GetSessionExecution(t.Context(), parent.ID)
	t.Logf("parent_execution_lookup=%v parent_context_cause=%v", parentErr, context.Cause(parentCtx))
	if parentErr != nil {
		t.Fatal("child session finished its parent's execution lease")
	}
}

// D4: the run's deferred transcript must land in the finish transaction —
// history, run state, and outbox are either all visible or none are.
func TestWorkerDeferredHistoryJoinsFinishTransaction(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	q := sqlc.New(db)
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "d4-agent", Name: "d4", Scope: "system", Enabled: true, Workspace: t.TempDir(), Sandbox: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{ID: uuid.Must(uuid.NewV7()).String(), SessionID: "d4-session", Kind: "chat", AgentID: pgtype.Text{String: "d4-agent", Valid: true}, UserID: pgtype.Text{String: "d4-user", Valid: true}, LastActive: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	info := session.Info{ID: "d4-session", AgentID: "d4-agent", UserID: "d4-user", Kind: "chat"}
	rt, err := New(Config{Memory: &recordingMemory{}, Execution: sessionexecution.New(db), NewRunner: func(context.Context, RunnerParams) (Runner, error) {
		return &chatFakeRunner{events: []Event{{Text: "deferred reply"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := run.New(db).Enqueue(ctx, tx, run.EnqueueParams{SessionID: info.ID, AgentID: info.AgentID, RequestKey: "d4-request", Actor: run.Actor{V: 1, Kind: "user", UserID: info.UserID}, Input: run.Input{V: 1, Text: "hello"}, ReplyAddress: run.ReplyAddress{V: 1, ChannelID: "ch-d4", AccountKey: "bot", ChatKey: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	lcmP, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := run.NewWorker(db, "d4-worker", reviewRuntimeExecutor{rt: rt, info: info},
		func(ctx context.Context, tx pgx.Tx, r sqlc.AgentRun, result, reply string) error {
			if result != "success" || reply == "" {
				return nil
			}
			ops, err := outbox.ReplyOps(r.ID, outbox.DeliveryKeyForRun(r.ID), "ch-d4", "bot", outbox.Address{V: 1, ChatKey: "chat"}, reply, 4000)
			if err != nil {
				return err
			}
			return outbox.New(db).Append(ctx, tx, ops)
		},
		run.WithTurnAppender(func(ctx context.Context, tx pgx.Tx, sess memory.Session, msgs []ai.Message) error {
			return lcmP.AppendSessionTurn(ctx, sqlc.New(tx), sess, msgs...)
		}))
	if _, err := w.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := run.New(db).Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var msgs int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_message m JOIN ctx_conversation c ON c.id=m.conversation_id WHERE c.session_id='d4-session'`).Scan(&msgs); err != nil {
		t.Fatal(err)
	}
	var outboxCount int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM channel_outbox WHERE run_id=$1", r.ID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if got.State != "completed" || msgs == 0 || outboxCount != 1 {
		t.Fatalf("run=%s history=%d outbox=%d — final history must commit with the run", got.State, msgs, outboxCount)
	}
}

// D4 failure side: a failing history append must fail the whole finish —
// no completed run, no outbox, no visible history.
func TestWorkerDeferredHistoryFailureBlocksCompletion(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	q := sqlc.New(db)
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "d4f-agent", Name: "d4f", Scope: "system", Enabled: true, Workspace: t.TempDir(), Sandbox: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{ID: uuid.Must(uuid.NewV7()).String(), SessionID: "d4f-session", Kind: "chat", AgentID: pgtype.Text{String: "d4f-agent", Valid: true}, UserID: pgtype.Text{String: "d4f-user", Valid: true}, LastActive: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	info := session.Info{ID: "d4f-session", AgentID: "d4f-agent", UserID: "d4f-user", Kind: "chat"}
	rt, err := New(Config{Memory: &recordingMemory{}, Execution: sessionexecution.New(db), NewRunner: func(context.Context, RunnerParams) (Runner, error) {
		return &chatFakeRunner{events: []Event{{Text: "never visible"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := run.New(db).Enqueue(ctx, tx, run.EnqueueParams{SessionID: info.ID, AgentID: info.AgentID, RequestKey: "d4f-request", Actor: run.Actor{V: 1, Kind: "user", UserID: info.UserID}, Input: run.Input{V: 1, Text: "hello"}, ReplyAddress: run.ReplyAddress{V: 1, ChannelID: "ch-d4f", AccountKey: "bot", ChatKey: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	outboxWrites := 0
	w := run.NewWorker(db, "d4f-worker", reviewRuntimeExecutor{rt: rt, info: info},
		func(ctx context.Context, tx pgx.Tx, r sqlc.AgentRun, result, reply string) error {
			outboxWrites++
			return nil
		},
		run.WithTurnAppender(func(context.Context, pgx.Tx, memory.Session, []ai.Message) error {
			return errors.New("history append failed")
		}))
	_, err = w.ProcessOnce(ctx)
	if err == nil {
		t.Fatal("finish must surface the history append failure")
	}
	got, err := run.New(db).Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var msgs, outboxCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_message m JOIN ctx_conversation c ON c.id=m.conversation_id WHERE c.session_id='d4f-session'`).Scan(&msgs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM channel_outbox WHERE run_id=$1", r.ID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if got.State == "completed" || msgs > 0 || outboxCount > 0 {
		t.Fatalf("failed finish must leave nothing visible: run=%s history=%d outbox=%d", got.State, msgs, outboxCount)
	}
}

// D4 rollback proof: the history append AND the outbox write both succeed
// inside the finish transaction, then a later failure rolls the whole thing
// back — no run state, no committed history, no outbox row is visible.
func TestWorkerOutboxFailureRollsBackCommittedHistory(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	q := sqlc.New(db)
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "d4r-agent", Name: "d4r", Scope: "system", Enabled: true, Workspace: t.TempDir(), Sandbox: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{ID: uuid.Must(uuid.NewV7()).String(), SessionID: "d4r-session", Kind: "chat", AgentID: pgtype.Text{String: "d4r-agent", Valid: true}, UserID: pgtype.Text{String: "d4r-user", Valid: true}, LastActive: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	info := session.Info{ID: "d4r-session", AgentID: "d4r-agent", UserID: "d4r-user", Kind: "chat"}
	rt, err := New(Config{Memory: &recordingMemory{}, Execution: sessionexecution.New(db), NewRunner: func(context.Context, RunnerParams) (Runner, error) {
		return &chatFakeRunner{events: []Event{{Text: "rolled back reply"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := run.New(db).Enqueue(ctx, tx, run.EnqueueParams{SessionID: info.ID, AgentID: info.AgentID, RequestKey: "d4r-request", Actor: run.Actor{V: 1, Kind: "user", UserID: info.UserID}, Input: run.Input{V: 1, Text: "hello"}, ReplyAddress: run.ReplyAddress{V: 1, ChannelID: "ch-d4r", AccountKey: "bot", ChatKey: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	lcmP, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := run.NewWorker(db, "d4r-worker", reviewRuntimeExecutor{rt: rt, info: info},
		func(ctx context.Context, tx pgx.Tx, r sqlc.AgentRun, result, reply string) error {
			if result != "success" || reply == "" {
				return nil
			}
			ops, err := outbox.ReplyOps(r.ID, outbox.DeliveryKeyForRun(r.ID), "ch-d4r", "bot", outbox.Address{V: 1, ChatKey: "chat"}, reply, 4000)
			if err != nil {
				return err
			}
			if err := outbox.New(db).Append(ctx, tx, ops); err != nil {
				return err
			}
			return errors.New("finish failed after real outbox write")
		},
		run.WithTurnAppender(func(ctx context.Context, tx pgx.Tx, sess memory.Session, msgs []ai.Message) error {
			return lcmP.AppendSessionTurn(ctx, sqlc.New(tx), sess, msgs...)
		}))
	_, err = w.ProcessOnce(ctx)
	if err == nil {
		t.Fatal("finish must surface the outbox failure")
	}
	got, err := run.New(db).Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var msgs, outboxCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_message m JOIN ctx_conversation c ON c.id=m.conversation_id WHERE c.session_id='d4r-session'`).Scan(&msgs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM channel_outbox WHERE run_id=$1", r.ID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if got.State == "completed" || msgs > 0 || outboxCount > 0 {
		t.Fatalf("outbox failure must roll back the history it committed: run=%s history=%d outbox=%d", got.State, msgs, outboxCount)
	}
}

// countingExecutor wraps the real-Runtime executor and records invocations so
// the test can prove an already-finished run is never executed twice.
type countingExecutor struct {
	inner reviewRuntimeExecutor
	calls int
}

func (e *countingExecutor) Execute(ctx context.Context, r sqlc.AgentRun) (string, error) {
	e.calls++
	return e.inner.Execute(ctx, r)
}

// A committed finish is the durable truth: a recovering worker (fresh
// instance) reclaims nothing and never re-executes a completed turn — the
// run reads back completed with its history and pending outbox row. This is
// not commit-fault injection: the real lost-commit-acknowledgment case lives
// at the lease layer (sessionexecution's commit seam is package-internal and
// NewWorker owns its store, so injecting here would widen the production
// API). See TestFinishLostCommitAcknowledgmentCommitsExtra.
func TestWorkerCompletedRunIsNotReexecuted(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	q := sqlc.New(db)
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "d4u-agent", Name: "d4u", Scope: "system", Enabled: true, Workspace: t.TempDir(), Sandbox: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{ID: uuid.Must(uuid.NewV7()).String(), SessionID: "d4u-session", Kind: "chat", AgentID: pgtype.Text{String: "d4u-agent", Valid: true}, UserID: pgtype.Text{String: "d4u-user", Valid: true}, LastActive: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	info := session.Info{ID: "d4u-session", AgentID: "d4u-agent", UserID: "d4u-user", Kind: "chat"}
	rt, err := New(Config{Memory: &recordingMemory{}, Execution: sessionexecution.New(db), NewRunner: func(context.Context, RunnerParams) (Runner, error) {
		return &chatFakeRunner{events: []Event{{Text: "committed reply"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := run.New(db).Enqueue(ctx, tx, run.EnqueueParams{SessionID: info.ID, AgentID: info.AgentID, RequestKey: "d4u-request", Actor: run.Actor{V: 1, Kind: "user", UserID: info.UserID}, Input: run.Input{V: 1, Text: "hello"}, ReplyAddress: run.ReplyAddress{V: 1, ChannelID: "ch-d4u", AccountKey: "bot", ChatKey: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	lcmP, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &countingExecutor{inner: reviewRuntimeExecutor{rt: rt, info: info}}
	finish := func(ctx context.Context, tx pgx.Tx, r sqlc.AgentRun, result, reply string) error {
		if result != "success" || reply == "" {
			return nil
		}
		ops, err := outbox.ReplyOps(r.ID, outbox.DeliveryKeyForRun(r.ID), "ch-d4u", "bot", outbox.Address{V: 1, ChatKey: "chat"}, reply, 4000)
		if err != nil {
			return err
		}
		return outbox.New(db).Append(ctx, tx, ops)
	}
	appendHistory := func(ctx context.Context, tx pgx.Tx, sess memory.Session, msgs []ai.Message) error {
		return lcmP.AppendSessionTurn(ctx, sqlc.New(tx), sess, msgs...)
	}
	w := run.NewWorker(db, "d4u-worker", exec, finish, run.WithTurnAppender(appendHistory))
	if _, err := w.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// A fresh worker — the recovering replica — reads the committed state
	// and must find the run already finished, with nothing to redo.
	recovery := run.NewWorker(db, "d4u-recovery", exec, finish, run.WithTurnAppender(appendHistory))
	claimed, err := recovery.ProcessOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := run.New(db).Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var msgs, outboxCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_message m JOIN ctx_conversation c ON c.id=m.conversation_id WHERE c.session_id='d4u-session'`).Scan(&msgs); err != nil {
		t.Fatal(err)
	}
	var pendingOutbox int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM channel_outbox WHERE run_id=$1 AND state='pending'", r.ID).Scan(&pendingOutbox); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM channel_outbox WHERE run_id=$1", r.ID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if claimed || exec.calls != 1 || got.State != "completed" || msgs == 0 || outboxCount != 1 || pendingOutbox != 1 {
		t.Fatalf("a committed run must never re-execute: claimed=%v exec_calls=%d run=%s history=%d outbox=%d pending=%d", claimed, exec.calls, got.State, msgs, outboxCount, pendingOutbox)
	}
}
