package sessionevent

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func createSession(t *testing.T, db *pgxpool.Pool, sessionID string) {
	t.Helper()
	if _, err := sqlc.New(db).CreateConversation(t.Context(), sqlc.CreateConversationParams{
		ID: uuid.Must(uuid.NewV7()).String(), SessionID: sessionID, Kind: "chat",
		LastActive: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
}

func TestAppendAssignsContiguousSeq(t *testing.T) {
	execA := uuid.Must(uuid.NewV7()).String()
	db := dbtest.New(t)
	createSession(t, db, "s-1")
	s := New(db)
	ctx := t.Context()

	// Concurrent appends from "two replicas" — same store, the advisory lock
	// is what keeps seq contiguous.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if err := s.Append(ctx, "s-1", execA, "", json.RawMessage(`{"text":"x"}`)); err != nil {
				t.Errorf("append %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	evs, err := s.ReadSince(ctx, "s-1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 8 {
		t.Fatalf("events = %d", len(evs))
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("seq[%d] = %d, want %d", i, ev.Seq, i+1)
		}
	}
}

func TestReadForRunAndOpenRun(t *testing.T) {
	execA := uuid.Must(uuid.NewV7()).String()
	execB := uuid.Must(uuid.NewV7()).String()
	db := dbtest.New(t)
	createSession(t, db, "s-1")
	s := New(db)
	ctx := t.Context()

	runID := uuid.Must(uuid.NewV7()).String()
	// Linkable run row needs the session + agent scaffolding; insert directly.
	if _, err := sqlc.New(db).CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "a-1", Name: "a-1", Workspace: t.TempDir(), Sandbox: json.RawMessage("{}"), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO agent_run (id, session_id, agent_id, request_key, enqueue_seq, state, actor, input, reply_address)
		 VALUES ($1, 's-1', 'a-1', 'k-1', 1, 'running', '{}', '{}', '{}')`, runID); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, "s-1", execA, runID, json.RawMessage(`{"text":"one"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, "s-1", execB, "", json.RawMessage(`{"text":"other-run"}`)); err != nil {
		t.Fatal(err)
	}

	open, err := s.OpenRunID(ctx, "s-1")
	if err != nil || open != runID {
		t.Fatalf("open run = %q err=%v", open, err)
	}
	evs, err := s.ReadForRun(ctx, "s-1", runID, 0, 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("run events = %d err=%v", len(evs), err)
	}
	if evs[0].RunID != runID {
		t.Fatalf("run link = %q", evs[0].RunID)
	}
	// Cursor skips it.
	evs, err = s.ReadForRun(ctx, "s-1", runID, evs[0].Seq, 10)
	if err != nil || len(evs) != 0 {
		t.Fatalf("after cursor = %d", len(evs))
	}
}

// Sequence numbers come from the session row's counter: pruning the log must
// never rewind them or an old cursor goes blind to new events.
func TestSequenceSurvivesPrune(t *testing.T) {
	execA := uuid.Must(uuid.NewV7()).String()
	db := dbtest.New(t)
	createSession(t, db, "prune-session")
	s := New(db)
	ctx := t.Context()
	for range 3 {
		if err := s.Append(ctx, "prune-session", execA, "", json.RawMessage(`{"text":"old"}`)); err != nil {
			t.Fatal(err)
		}
	}
	// The execution's terminal marker retires its segment: without it the
	// events would be kept forever regardless of age.
	if err := s.Append(ctx, "prune-session", execA, "", json.RawMessage(`{"type":"turn_terminal","result":"success"}`)); err != nil {
		t.Fatal(err)
	}
	before, err := s.LatestSeq(ctx, "prune-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "UPDATE ctx_session_event SET created_at=clock_timestamp()-interval '25 hours' WHERE session_id='prune-session'"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Prune(ctx); err != nil || n != 4 {
		t.Fatalf("prune=%d err=%v", n, err)
	}
	if err := s.Append(ctx, "prune-session", "", "", json.RawMessage(`{"text":"new"}`)); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ReadSince(ctx, "prune-session", before, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatal("retention rewound the sequence; the old cursor cannot see the new event")
	}
}

// TestOpenExecutionPrefersRunIdentity proves the watch resolution keeps a
// run-linked execution on the run's cursor space: a POST-streamed turn and a
// sibling GET reconnect must share "runID:seq", not switch to the token's.
func TestOpenExecutionPrefersRunIdentity(t *testing.T) {
	db := dbtest.New(t)
	createSession(t, db, "s-1")
	s := New(db)
	ctx := t.Context()

	token := uuid.Must(uuid.NewV7()).String()
	if _, err := sqlc.New(db).ClaimSessionExecution(ctx, sqlc.ClaimSessionExecutionParams{
		SessionID: "s-1", Token: token,
	}); err != nil {
		t.Fatal(err)
	}
	// Non-run execution: token only.
	gotTok, gotRun, err := s.OpenExecution(ctx, "s-1")
	if err != nil || gotTok != token || gotRun != "" {
		t.Fatalf("open execution = (%q,%q) err=%v", gotTok, gotRun, err)
	}

	if _, err := sqlc.New(db).CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "a-1", Name: "a-1", Workspace: t.TempDir(), Sandbox: json.RawMessage("{}"), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	runID := uuid.Must(uuid.NewV7()).String()
	if _, err := db.Exec(ctx,
		`INSERT INTO agent_run (id, session_id, agent_id, request_key, enqueue_seq, state, actor, input, reply_address)
		 VALUES ($1, 's-1', 'a-1', 'k-1', 1, 'running', '{}', '{}', '{}')`, runID); err != nil {
		t.Fatal(err)
	}
	if n, err := sqlc.New(db).SetSessionExecutionRun(ctx, sqlc.SetSessionExecutionRunParams{
		SessionID: "s-1", Token: token, RunID: pgtype.Text{String: runID, Valid: true},
	}); err != nil || n != 1 {
		t.Fatalf("link run: %d %v", n, err)
	}
	gotTok, gotRun, err = s.OpenExecution(ctx, "s-1")
	if err != nil || gotTok != token || gotRun != runID {
		t.Fatalf("run-linked execution = (%q,%q) err=%v, want token+runID", gotTok, gotRun, err)
	}
}

// TestExecutionTailFindsTerminalMarker covers the non-run tail contract:
// events stamped with the token replay in order and the turn_terminal marker
// ends the watch with its verdict.
func TestExecutionTailFindsTerminalMarker(t *testing.T) {
	db := dbtest.New(t)
	createSession(t, db, "s-1")
	s := New(db)
	ctx := t.Context()
	token := uuid.Must(uuid.NewV7()).String()

	if err := s.Append(ctx, "s-1", token, "", json.RawMessage(`{"text":"one"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, "s-1", token, "", json.RawMessage(`{"type":"turn_terminal","result":"success","reason":""}`)); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ReadForExecution(ctx, "s-1", token, 0, 10)
	if err != nil || len(evs) != 2 {
		t.Fatalf("execution events = %d err=%v", len(evs), err)
	}
	result, _, ok, err := s.ExecutionTerminal(ctx, "s-1", token)
	if err != nil || !ok || result != "success" {
		t.Fatalf("terminal = (%q,%v) err=%v", result, ok, err)
	}
	// A different token on the same session has no terminal.
	if _, _, ok, err := s.ExecutionTerminal(ctx, "s-1", uuid.Must(uuid.NewV7()).String()); err != nil || ok {
		t.Fatalf("foreign token terminal = %v err=%v, want false", ok, err)
	}
}

// TestPruneAnchorsSegmentToTerminalAge pins the retention contract: an
// execution's events are kept as a whole segment until the turn's own
// terminal marker ages past the window — an old start with a fresh terminal
// still retains the full replay a bootstrapping observer needs.
func TestPruneAnchorsSegmentToTerminalAge(t *testing.T) {
	db := dbtest.New(t)
	createSession(t, db, "s-1")
	s := New(db)
	ctx := t.Context()
	token := uuid.Must(uuid.NewV7()).String()

	// Ancient events under an open execution: kept.
	for range 3 {
		if err := s.Append(ctx, "s-1", token, "", json.RawMessage(`{"text":"x"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, "UPDATE ctx_session_event SET created_at=clock_timestamp()-interval '25 hours'"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Prune(ctx); err != nil || n != 0 {
		t.Fatalf("active execution segment pruned: %d %v", n, err)
	}

	// A just-landed terminal keeps the whole segment — its age, not the
	// events', anchors the window.
	if err := s.Append(ctx, "s-1", token, "", json.RawMessage(`{"type":"turn_terminal","result":"success"}`)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Prune(ctx); err != nil || n != 0 {
		t.Fatalf("segment with fresh terminal pruned: %d %v", n, err)
	}

	// Once the terminal itself is old, the entire segment goes together.
	if _, err := db.Exec(ctx, "UPDATE ctx_session_event SET created_at=clock_timestamp()-interval '25 hours' WHERE event->>'type'='turn_terminal'"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Prune(ctx); err != nil || n != 4 {
		t.Fatalf("aged segment prune = %d %v, want 4", n, err)
	}
	evs, err := s.ReadForExecution(ctx, "s-1", token, 0, 10)
	if err != nil || len(evs) != 0 {
		t.Fatalf("residual events = %d", len(evs))
	}
}

// TestAppendRejectedAfterTerminal proves a fenced-out writer cannot extend a
// turn whose marker already landed — the paused-writer-resumes gap.
func TestAppendRejectedAfterTerminal(t *testing.T) {
	db := dbtest.New(t)
	createSession(t, db, "s-1")
	s := New(db)
	ctx := t.Context()
	token := uuid.Must(uuid.NewV7()).String()

	if err := s.Append(ctx, "s-1", token, "", json.RawMessage(`{"text":"one"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, "s-1", token, "", json.RawMessage(`{"type":"turn_terminal","result":"error","reason":"reaped"}`)); err != nil {
		t.Fatal(err)
	}
	err := s.Append(ctx, "s-1", token, "", json.RawMessage(`{"text":"late"}`))
	if !errors.Is(err, ErrExecutionTerminated) {
		t.Fatalf("post-terminal append = %v, want ErrExecutionTerminated", err)
	}
	// Another token on the same session is unaffected.
	if err := s.Append(ctx, "s-1", uuid.Must(uuid.NewV7()).String(), "", json.RawMessage(`{"text":"next"}`)); err != nil {
		t.Fatal(err)
	}
}

// TestTurnStartFencedAndBoundedHistory proves the start marker (a) requires a
// live, exact session+token execution row, (b) records the canonical history
// watermark from ctx_message.conversation_id — which is deliberately not the
// session id in production.
func TestTurnStartFencedAndBoundedHistory(t *testing.T) {
	db := dbtest.New(t)
	s := New(db)
	ctx := t.Context()

	// Conversation id intentionally differs from session id — the boundary
	// must come from the conversation, not the session key.
	convID := uuid.Must(uuid.NewV7()).String()
	if _, err := sqlc.New(db).CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: convID, SessionID: "s-1", Kind: "chat", LastActive: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(db)
	for i, role := range []string{"user", "assistant"} {
		if _, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
			ID: uuid.NewString(), ConversationID: convID, Seq: int64(i + 1),
			Role: role, EventType: "message", Content: "{}", ActorType: "user",
		}); err != nil {
			t.Fatal(err)
		}
	}

	token := uuid.Must(uuid.NewV7()).String()
	// No execution row at all → fenced out.
	if err := s.TurnStart(ctx, "s-1", token, ""); !errors.Is(err, ErrExecutionTerminated) {
		t.Fatalf("unclaimed TurnStart = %v, want ErrExecutionTerminated", err)
	}
	if _, err := q.ClaimSessionExecution(ctx, sqlc.ClaimSessionExecutionParams{
		SessionID: "s-1", Token: token,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.TurnStart(ctx, "s-1", token, ""); err != nil {
		t.Fatal(err)
	}
	boundary, ok, err := s.TurnStartBoundary(ctx, "s-1", token)
	if err != nil || !ok || boundary != 2 {
		t.Fatalf("boundary = (%d,%v) err=%v, want (2,true)", boundary, ok, err)
	}
	// Idempotent: a second call does not move the boundary or reinsert.
	if _, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
		ID: uuid.NewString(), ConversationID: convID, Seq: 3,
		Role: "user", EventType: "message", Content: "{}", ActorType: "user",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.TurnStart(ctx, "s-1", token, ""); err != nil {
		t.Fatal(err)
	}
	if boundary, _, _ := s.TurnStartBoundary(ctx, "s-1", token); boundary != 2 {
		t.Fatalf("second TurnStart moved boundary to %d", boundary)
	}

	// Expired token: fenced out of minting a new boundary.
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: uuid.Must(uuid.NewV7()).String(), SessionID: "s-2", Kind: "chat", LastActive: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	token2 := uuid.Must(uuid.NewV7()).String()
	if _, err := q.ClaimSessionExecution(ctx, sqlc.ClaimSessionExecutionParams{
		SessionID: "s-2", Token: token2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx,
		"UPDATE ctx_session_execution SET lease_until = clock_timestamp() - interval '1 second' WHERE session_id='s-2'"); err != nil {
		t.Fatal(err)
	}
	if err := s.TurnStart(ctx, "s-2", token2, ""); !errors.Is(err, ErrExecutionTerminated) {
		t.Fatalf("expired TurnStart = %v, want ErrExecutionTerminated", err)
	}
}
