package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/sessionexecution"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func executionFixture(t *testing.T, db *pgxpool.Pool) session.Info {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	_, err := sqlc.New(db).CreateConversation(t.Context(), sqlc.CreateConversationParams{ID: id, SessionID: id, Kind: "chat", LastActive: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return session.Info{ID: id, UserID: "user", AgentID: "agent"}
}

func TestExecutionIsClaimedBeforePrepareAndAbortFinishesIt(t *testing.T) {
	db := dbtest.New(t)
	info := executionFixture(t, db)
	execution := sessionexecution.New(db)
	calls := 0
	cfg := Config{Memory: &recordingMemory{}, Execution: execution, NewRunner: func(context.Context, RunnerParams) (Runner, error) { calls++; return &chatFakeRunner{}, nil }}
	first, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	second, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	admission, err := first.BeginChatAdmission(t.Context(), info, "prepare", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.AbortChatAdmission(admission)
	if calls != 0 {
		t.Fatal("slow preparation ran during claim")
	}
	row, err := sqlc.New(db).GetSessionExecution(t.Context(), info.ID)
	if err != nil || row.Token == "" {
		t.Fatalf("missing execution: %v", err)
	}
	if _, err := second.BeginChatAdmission(t.Context(), info, "duplicate", nil); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("second admission: %v", err)
	}
	first.AbortChatAdmission(admission)
	if _, err := sqlc.New(db).GetSessionExecution(t.Context(), info.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("abort retained token: %v", err)
	}
	var result string
	if err := db.QueryRow(t.Context(), "SELECT last_turn_result FROM ctx_conversation WHERE session_id=$1", info.ID).Scan(&result); err != nil || result != "error" {
		t.Fatalf("result=%s err=%v", result, err)
	}
}

func TestDatabaseLeaseCoversGroupCommitAndEndsBeforeCallerDelivery(t *testing.T) {
	db := dbtest.New(t)
	info := executionFixture(t, db)
	info.GroupID = uuid.Must(uuid.NewV7()).String()
	info.UserID = info.GroupID
	store := sessionexecution.New(db)
	rt, err := New(Config{Memory: &recordingMemory{}, Execution: store, NewRunner: func(context.Context, RunnerParams) (Runner, error) {
		return &chatFakeRunner{events: []Event{{Text: "reply"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Close() }()
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	commit := &testGroupResult{commit: func(ctx context.Context, _ memory.DeferredGroupTurn) error { entered <- ctx; <-release; return nil }}
	stream, err := rt.ChatAdmitted(WithGroupResultCommitter(t.Context(), commit), info, "hello")
	if err != nil {
		t.Fatal(err)
	}
	var runCtx context.Context
	select {
	case runCtx = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("commit did not start")
	}
	if sessionexecution.FromContext(runCtx) == nil {
		t.Fatal("commit lost token")
	}
	if err := sessionexecution.FromContext(runCtx).Renew(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Claim(t.Context(), info.ID); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("claim during result commit: %v", err)
	}
	unblock()
	for event := range stream {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
	// No platform delivery acknowledgment is required after EOF.
	_, next, err := store.Claim(t.Context(), info.ID)
	if err != nil {
		t.Fatalf("caller delivery still held execution: %v", err)
	}
	if err := next.Finish("success"); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseLossReachesCallerAndCannotFinishSuccessor(t *testing.T) {
	db := dbtest.New(t)
	info := executionFixture(t, db)
	store := sessionexecution.New(db)
	entered := make(chan context.Context, 1)
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	rt, err := New(Config{Memory: &recordingMemory{}, Execution: store, NewRunner: func(ctx context.Context, _ RunnerParams) (Runner, error) {
		entered <- ctx
		return &blockingRunner{gate: gate}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Close() }()
	stream, err := rt.ChatAdmitted(t.Context(), info, "hello")
	if err != nil {
		t.Fatal(err)
	}
	runCtx := <-entered
	if _, err := db.Exec(t.Context(), "UPDATE ctx_session_execution SET lease_until=clock_timestamp()-interval '1 second' WHERE session_id=$1", info.ID); err != nil {
		t.Fatal(err)
	}
	_, successor, err := store.Claim(t.Context(), info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = successor.Finish("error") }()
	_, writeErr := sessionexecution.Begin(context.WithoutCancel(runCtx), db)
	if !errors.Is(writeErr, sessionexecution.ErrLost) {
		t.Fatalf("old writer: %v", writeErr)
	}
	release()
	var got error
	for event := range stream {
		got = errors.Join(got, event.Err)
	}
	if !errors.Is(got, sessionexecution.ErrLost) {
		t.Fatalf("caller did not receive loss: %v", got)
	}
	if err := successor.Finish("success"); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseLossReachesCallerWithFullOutputBuffer(t *testing.T) {
	db := dbtest.New(t)
	info := executionFixture(t, db)
	rt, err := New(Config{Memory: &recordingMemory{}, Execution: sessionexecution.New(db), NewRunner: func(context.Context, RunnerParams) (Runner, error) {
		return &chatFakeRunner{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Close() }()
	admission, err := rt.BeginChatAdmission(t.Context(), info, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.AbortChatAdmission(admission)
	for range cap(admission.out) {
		admission.out <- Event{Text: "partial"}
	}
	sessionexecution.Abort(admission.ctx, errors.New("database unavailable"))
	inner := make(chan Event)
	close(inner)
	producerResult := make(chan memory.SessionTurnResult, 1)
	producerResult <- memory.SessionTurnCanceled
	admission.published = true
	rt.hub.begin(info.ID)
	rt.runChatForwarder(admission, inner, producerResult)
	var got error
	for event := range admission.out {
		got = errors.Join(got, event.Err)
	}
	if !errors.Is(got, sessionexecution.ErrLost) {
		t.Fatalf("full buffer hid terminal loss: %v", got)
	}
}
