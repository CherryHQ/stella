package run

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func createAgent(t *testing.T, db *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := sqlc.New(db).CreateAgent(context.Background(), sqlc.CreateAgentParams{
		ID: id, Name: id, Workspace: t.TempDir(), Sandbox: json.RawMessage("{}"), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
}

func createSession(t *testing.T, db *pgxpool.Pool, sessionID, agentID string) {
	t.Helper()
	if _, err := sqlc.New(db).CreateConversation(context.Background(), sqlc.CreateConversationParams{
		ID: uuid.Must(uuid.NewV7()).String(), SessionID: sessionID, Kind: "chat",
		LastActive: time.Now().UTC(),
		AgentID:    pgtype.Text{String: agentID, Valid: true},
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
}

// lockSession holds the ctx_conversation row lock, as every real enqueue path
// must, then runs fn inside the same transaction.
func lockSession(t *testing.T, db *pgxpool.Pool, sessionID string, fn func(tx pgx.Tx) error) error {
	t.Helper()
	tx, err := db.Begin(t.Context())
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(),
		"SELECT id FROM ctx_conversation WHERE session_id = $1 FOR UPDATE", sessionID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(t.Context())
}

func enqueueParams(sessionID, key string) EnqueueParams {
	return EnqueueParams{
		SessionID:    sessionID,
		AgentID:      "agent-1",
		RequestKey:   key,
		Actor:        Actor{V: EnvelopeVersion, Kind: "user", Platform: "telegram", PlatformID: "u-1"},
		Input:        Input{V: EnvelopeVersion, Kind: "message", Text: "hi"},
		ReplyAddress: ReplyAddress{V: EnvelopeVersion, ChannelID: "ch-1", AccountKey: "bot-1", ChatKey: "chat-a"},
	}
}

func TestEnqueueDeduplicatesRequestKey(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	s := New(db)

	const n = 4
	var wg sync.WaitGroup
	created := make([]bool, n)
	ids := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := lockSession(t, db, "sess-1", func(tx pgx.Tx) error {
				row, c, err := s.Enqueue(t.Context(), tx, enqueueParams("sess-1", "req-1"))
				created[i] = c
				ids[i] = row.ID
				return err
			})
			if err != nil {
				t.Errorf("enqueue %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	wins := 0
	for i := range n {
		if created[i] {
			wins++
		}
		if ids[i] == "" || ids[i] != ids[0] {
			t.Fatalf("duplicate key produced different run: %q vs %q", ids[i], ids[0])
		}
	}
	if wins != 1 {
		t.Fatalf("want exactly one created run, got %d", wins)
	}
}

func TestEnqueueSeqIsMonotonicUnderLock(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	s := New(db)

	const n = 6
	seqs := make(chan int64, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := lockSession(t, db, "sess-1", func(tx pgx.Tx) error {
				row, _, err := s.Enqueue(t.Context(), tx, enqueueParams("sess-1", "req-"+string(rune('a'+i))))
				if err == nil {
					seqs <- row.EnqueueSeq
				}
				return err
			})
			if err != nil {
				t.Errorf("enqueue %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(seqs)

	got := make([]int64, 0, n)
	for v := range seqs {
		got = append(got, v)
	}
	slices.Sort(got)
	for i, v := range got {
		if v != int64(i+1) {
			t.Fatalf("enqueue_seq not contiguous 1..n: %v", got)
		}
	}
}

func TestStartFinishFencing(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	s := New(db)
	ctx := t.Context()

	var runID string
	err := lockSession(t, db, "sess-1", func(tx pgx.Tx) error {
		row, _, err := s.Enqueue(ctx, tx, enqueueParams("sess-1", "req-1"))
		runID = row.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	begin := func() pgx.Tx {
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}

	// Only one worker can start it.
	tx1 := begin()
	defer func() { _ = tx1.Rollback(ctx) }()
	ok, err := s.Start(ctx, tx1, runID, "worker-a")
	if err != nil || !ok {
		t.Fatalf("start a: ok=%v err=%v", ok, err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx2 := begin()
	defer func() { _ = tx2.Rollback(ctx) }()
	defer func() { _ = tx2.Rollback(ctx) }()
	ok, err = s.Start(ctx, tx2, runID, "worker-b")
	if err != nil || ok {
		t.Fatalf("second start should fail: ok=%v err=%v", ok, err)
	}

	// Wrong worker cannot finish; wrong state rejected before hitting the DB.
	tx3 := begin()
	defer func() { _ = tx3.Rollback(ctx) }()
	ok, err = s.Finish(ctx, tx3, runID, "worker-b", StateCompleted, "")
	if err != nil || ok {
		t.Fatalf("foreign worker finish should fail: ok=%v err=%v", ok, err)
	}
	if _, err := s.Finish(ctx, tx3, runID, "worker-a", StateQueued, ""); err == nil {
		t.Fatal("non-terminal finish accepted")
	}
	_ = tx3.Rollback(ctx)

	tx4 := begin()
	defer func() { _ = tx4.Rollback(ctx) }()
	ok, err = s.Finish(ctx, tx4, runID, "worker-a", StateCompleted, "")
	if err != nil || !ok {
		t.Fatalf("finish: ok=%v err=%v", ok, err)
	}
	if err := tx4.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Terminal is one-way.
	tx5 := begin()
	defer func() { _ = tx5.Rollback(ctx) }()
	defer func() { _ = tx5.Rollback(ctx) }()
	if ok, _ := s.Finish(ctx, tx5, runID, "worker-a", StateFailed, "x"); ok {
		t.Fatal("terminal run moved again")
	}
	if ok, _ := s.CancelQueued(ctx, tx5, runID); ok {
		t.Fatal("finished run canceled")
	}
}

func TestOneRunningPerSession(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	s := New(db)
	ctx := t.Context()

	var ids []string
	err := lockSession(t, db, "sess-1", func(tx pgx.Tx) error {
		for _, k := range []string{"req-1", "req-2"} {
			row, _, err := s.Enqueue(ctx, tx, enqueueParams("sess-1", k))
			if err != nil {
				return err
			}
			ids = append(ids, row.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	tx1, _ := db.Begin(ctx)
	defer func() { _ = tx1.Rollback(ctx) }()
	if ok, err := s.Start(ctx, tx1, ids[0], "w"); err != nil || !ok {
		t.Fatalf("first start: ok=%v err=%v", ok, err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx2, _ := db.Begin(ctx)
	defer func() { _ = tx2.Rollback(ctx) }()
	defer func() { _ = tx2.Rollback(ctx) }()
	if _, err := s.Start(ctx, tx2, ids[1], "w"); err == nil {
		t.Fatal("second running run on one session accepted")
	}
}

func TestReapExpiredMarksInterrupted(t *testing.T) {
	db := dbtest.New(t)
	createAgent(t, db, "agent-1")
	createSession(t, db, "sess-1", "agent-1")
	s := New(db)
	ctx := t.Context()

	var runID string
	err := lockSession(t, db, "sess-1", func(tx pgx.Tx) error {
		row, _, err := s.Enqueue(ctx, tx, enqueueParams("sess-1", "req-1"))
		runID = row.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := db.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	if ok, err := s.Start(ctx, tx, runID, "worker-a"); err != nil || !ok {
		t.Fatal("start")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Link a dead lease to the run: the worker vanished mid-execution.
	if _, err := db.Exec(ctx,
		`INSERT INTO ctx_session_execution (session_id, token, lease_until, run_id)
		 VALUES ($1, uuidv7(), clock_timestamp() - interval '1 second', $2)`,
		"sess-1", runID); err != nil {
		t.Fatal(err)
	}

	n, err := s.ReapExpired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	row, err := s.Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != string(StateInterrupted) {
		t.Fatalf("state %q, want interrupted", row.State)
	}
}

func TestByRequestKeyNotFound(t *testing.T) {
	db := dbtest.New(t)
	s := New(db)
	if _, err := s.ByRequestKey(t.Context(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
