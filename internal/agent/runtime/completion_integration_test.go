package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/runcontrol"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestRuntimeEOFKeepsLeaseUntilAdapterAck(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	userID := uuid.NewString()
	agentID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	store := agentrun.NewStoreWithLease(ctx, db, uuid.NewString(), time.Second)
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatalf("register boot: %v", err)
	}
	rt, err := New(Config{
		AgentRuns: store,
		Memory:    memorytest.New(),
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return &chatFakeRunner{events: []Event{{Text: "done"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	barrier := NewCompletionBarrier()
	info := session.Info{ID: sessionID, UserID: userID, AgentID: agentID}
	stream, err := rt.ChatAdmitted(ctx, info, "hello", WithCompletionBarrier(barrier))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	for event := range stream {
		if event.Err != nil {
			t.Fatalf("runtime event: %v", event.Err)
		}
	}

	run, running, err := store.Running(ctx, sessionID)
	if err != nil {
		t.Fatalf("read running run: %v", err)
	}
	if !running || run.CompletionState != "ready" || run.CompletionStatus != agentrun.StatusCompleted {
		t.Fatalf("after EOF run = running=%t state=%q status=%q", running, run.CompletionState, run.CompletionStatus)
	}
	guarded, err := barrier.Context(ctx)
	if err != nil {
		t.Fatalf("completion context: %v", err)
	}
	tx, err := db.Begin(guarded)
	if err != nil {
		t.Fatalf("begin guarded write: %v", err)
	}
	if err := agentrun.ValidateTx(guarded, tx); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("validate guarded write after EOF: %v", err)
	}
	if _, err := tx.Exec(guarded, `UPDATE ctx_conversation SET title = 'tail-write' WHERE session_id = $1`, sessionID); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("guarded write after EOF: %v", err)
	}
	if err := tx.Commit(guarded); err != nil {
		t.Fatalf("commit guarded write: %v", err)
	}

	if err := barrier.Ack(ctx, runcontrol.OutcomeDelivered); err != nil {
		t.Fatalf("ack delivered: %v", err)
	}
	select {
	case <-barrier.Done():
	case <-time.After(time.Second):
		t.Fatal("completion barrier did not close after Ack")
	}
	row, err := sqlc.New(db).GetAgentRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("read completed run: %v", err)
	}
	if row.Status != agentrun.StatusCompleted || row.CompletionState != "acked" || row.CompletionOutcome != string(runcontrol.OutcomeDelivered) {
		t.Fatalf("after Ack run = status=%q state=%q outcome=%q", row.Status, row.CompletionState, row.CompletionOutcome)
	}
}

func TestRuntimeStopClosesExternalCompletion(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	store := agentrun.NewStoreWithLease(ctx, db, uuid.NewString(), time.Second)
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	rt, err := New(Config{
		AgentRuns: store,
		Memory:    memorytest.New(),
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return &stopCompletionRunner{}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	barrier := NewCompletionBarrier()
	info := session.Info{ID: sessionID, UserID: uuid.NewString(), AgentID: uuid.NewString()}
	stream, err := rt.ChatAdmitted(ctx, info, "hello", WithCompletionBarrier(barrier))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for !rt.SessionLive(sessionID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !rt.SessionLive(sessionID) {
		t.Fatal("runtime turn did not become live")
	}
	run, running, err := store.Running(ctx, sessionID)
	if err != nil || !running {
		t.Fatalf("running AgentRun before stop = running=%t err=%v", running, err)
	}
	if !rt.StopSession(ctx, sessionID) {
		t.Fatal("StopSession reported no active turn")
	}
	for event := range stream {
		if event.Err != nil && !errors.Is(event.Err, context.Canceled) {
			t.Fatalf("runtime event: %v", event.Err)
		}
	}
	select {
	case <-barrier.Done():
	case <-time.After(time.Second):
		t.Fatal("external completion did not close after StopSession")
	}
	row, err := sqlc.New(db).GetAgentRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("read stopped run: %v", err)
	}
	if row.Status != agentrun.StatusAborted || row.AbortReason != "user_stop" {
		t.Fatalf("stopped run = status=%q abort_reason=%q", row.Status, row.AbortReason)
	}
}

type stopCompletionRunner struct{}

func (*stopCompletionRunner) Chat(ctx context.Context, _ []ai.Message, _ MessageContent) <-chan Event {
	out := make(chan Event, 2)
	out <- Event{Text: "partial"}
	go func() {
		<-ctx.Done()
		out <- Event{Err: context.Canceled}
		close(out)
	}()
	return out
}

func (*stopCompletionRunner) Alive() bool                  { return true }
func (*stopCompletionRunner) Busy() bool                   { return false }
func (*stopCompletionRunner) LastActivity() time.Time      { return time.Now() }
func (*stopCompletionRunner) SystemPrompt() string         { return "" }
func (*stopCompletionRunner) PluginContext() PluginContext { return PluginContext{} }
func (*stopCompletionRunner) Close() error                 { return nil }
