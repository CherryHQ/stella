package sessionevent

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
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
	db := dbtest.New(t)
	createSession(t, db, "s-1")
	s := New(db)
	ctx := t.Context()

	// Concurrent appends from "two replicas" — same store, the advisory lock
	// is what keeps seq contiguous.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if err := s.Append(ctx, "s-1", "", json.RawMessage(`{"text":"x"}`)); err != nil {
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
	if err := s.Append(ctx, "s-1", runID, json.RawMessage(`{"text":"one"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, "s-1", "", json.RawMessage(`{"text":"other-run"}`)); err != nil {
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
