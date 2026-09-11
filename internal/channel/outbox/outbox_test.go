package outbox

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func createChannel(t *testing.T, db *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := sqlc.New(db).CreateChannel(context.Background(), sqlc.CreateChannelParams{
		ID: id, Name: id, Type: "test",
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
}

func op(delivery string, index int) Op {
	return Op{
		DeliveryKey: delivery,
		Index:       index,
		Kind:        OpSendText,
		ChannelID:   "ch-1",
		AccountKey:  "bot-1",
		Address:     json.RawMessage(`{"v":1,"chat_key":"chat-a"}`),
		Payload:     json.RawMessage(`{"v":1,"text":"hi"}`),
	}
}

func TestAppendIsIdempotent(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	ops := []Op{op("d-1", 0), op("d-1", 1), op("d-1", 2)}
	for i := range 2 {
		tx, _ := db.Begin(ctx)
		defer func() { _ = tx.Rollback(ctx) }()
		if err := s.Append(ctx, tx, ops); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListByDelivery(ctx, "d-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("re-appending duplicated ops: %d rows", len(rows))
	}
}

func TestAttemptFencing(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	tx, _ := db.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.Append(ctx, tx, []Op{op("d-1", 0)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	tx, _ = db.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	due, err := s.ListDue(ctx, tx, "ch-1")
	if err != nil || len(due) != 1 {
		t.Fatalf("list due: %d err=%v", len(due), err)
	}
	attempt, ok, err := s.ClaimAttempt(ctx, tx, due[0].ID, "11111111-1111-4111-8111-111111111111")
	if err != nil || !ok || attempt == "" {
		t.Fatalf("claim: ok=%v token=%q err=%v", ok, attempt, err)
	}
	// Already claimed: a second owner gets nothing.
	if _, ok, err := s.ClaimAttempt(ctx, tx, due[0].ID, "22222222-2222-4222-8222-222222222222"); err != nil || ok {
		t.Fatalf("double claim: ok=%v err=%v", ok, err)
	}
	// Wrong attempt token cannot record the outcome.
	if ok, err := s.CompleteAttempt(ctx, tx, due[0].ID, "33333333-3333-4333-8333-333333333333", Outcome{State: StateSent}); err != nil || ok {
		t.Fatalf("foreign attempt complete: ok=%v err=%v", ok, err)
	}
	// The real attempt lands sent with the platform message id.
	if ok, err := s.CompleteAttempt(ctx, tx, due[0].ID, attempt, Outcome{
		State: StateSent, PlatformMessageID: "msg-42",
	}); err != nil || !ok {
		t.Fatalf("complete: ok=%v err=%v", ok, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	rows, _ := s.ListByDelivery(ctx, "d-1")
	if rows[0].State != StateSent || rows[0].PlatformMessageID.String != "msg-42" {
		t.Fatalf("state=%q platform_id=%q", rows[0].State, rows[0].PlatformMessageID.String)
	}
}

func TestExpireAttemptsAndRequeue(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	tx, _ := db.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.Append(ctx, tx, []Op{op("d-1", 0)}); err != nil {
		t.Fatal(err)
	}
	due, _ := s.ListDue(ctx, tx, "ch-1")
	if _, ok, err := s.ClaimAttempt(ctx, tx, due[0].ID, "44444444-4444-4444-8444-444444444444"); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Fresh attempt is not expired.
	if n, err := s.ExpireAttempts(ctx); err != nil || n != 0 {
		t.Fatalf("expire fresh: n=%d err=%v", n, err)
	}
	// Backdate past the 5-minute attempt deadline, then expire.
	if _, err := db.Exec(ctx,
		`UPDATE channel_outbox SET attempt_started_at = clock_timestamp() - interval '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ExpireAttempts(ctx); err != nil || n != 1 {
		t.Fatalf("expire stale: n=%d err=%v", n, err)
	}
	rows, _ := s.ListByDelivery(ctx, "d-1")
	if rows[0].State != StateUnknown {
		t.Fatalf("state %q, want unknown", rows[0].State)
	}

	// Probe decided the send never landed: requeue.
	tx, _ = db.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	later := time.Now().UTC().Add(time.Minute)
	if ok, err := s.Requeue(ctx, tx, rows[0].ID, &later); err != nil || !ok {
		t.Fatalf("requeue: ok=%v err=%v", ok, err)
	}
	// Deferred row is not due yet.
	if due, _ := s.ListDue(ctx, tx, "ch-1"); len(due) != 0 {
		t.Fatalf("deferred op listed as due")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCancelByRunLeavesHistory(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	tx, _ := db.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	ops := []Op{op("d-1", 0), op("d-1", 1)}
	ops[0].RunID, ops[1].RunID = "", "" // channel-level, no run needed for this test
	if err := s.Append(ctx, tx, ops); err != nil {
		t.Fatal(err)
	}
	due, _ := s.ListDue(ctx, tx, "ch-1")
	attempt, _, _ := s.ClaimAttempt(ctx, tx, due[0].ID, "44444444-4444-4444-8444-444444444444")
	if _, err := s.CompleteAttempt(ctx, tx, due[0].ID, attempt, Outcome{State: StateSent}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Cancel by run only touches ops owned by that run; these have none.
	tx, _ = db.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	if n, err := s.CancelByRun(ctx, tx, "55555555-5555-4555-8555-555555555555"); err != nil || n != 0 {
		t.Fatalf("cancel other run: n=%d err=%v", n, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListByDelivery(ctx, "d-1")
	if rows[0].State != StateSent || rows[1].State != StatePending {
		t.Fatalf("states %q/%q", rows[0].State, rows[1].State)
	}
}
