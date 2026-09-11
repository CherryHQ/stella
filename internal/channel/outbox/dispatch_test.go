package outbox

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// fakeSender scripts one platform response per call.
type fakeSender struct {
	results []sendResult
	calls   []pkgchannel.OutboundOp
}

type sendResult struct {
	receipt pkgchannel.SendResult
	err     error
}

func (f *fakeSender) SendOperation(_ context.Context, op pkgchannel.OutboundOp) (pkgchannel.SendResult, error) {
	f.calls = append(f.calls, op)
	if len(f.results) == 0 {
		return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendUnknown, "no scripted result")
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r.receipt, r.err
}

func appendOps(t *testing.T, s *Store, db *pgxpool.Pool, ops []Op) {
	t.Helper()
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if err := s.Append(t.Context(), tx, ops); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func outboxStates(t *testing.T, db *pgxpool.Pool) []string {
	t.Helper()
	rows, err := sqlc.New(db).GetChannelOutboxByDelivery(t.Context(), "d-1")
	if err != nil {
		t.Fatal(err)
	}
	states := make([]string, len(rows))
	for i, r := range rows {
		states[i] = r.State
	}
	return states
}

func TestDispatchSendsAndRecordsReceipt(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()
	appendOps(t, s, db, []Op{op("d-1", 0)})

	sender := &fakeSender{results: []sendResult{{receipt: pkgchannel.SendResult{PlatformMessageID: "tg-42"}}}}
	n, err := s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 1 {
		t.Fatalf("ProcessDue: n=%d err=%v", n, err)
	}
	rows, _ := s.ListByDelivery(ctx, "d-1")
	if rows[0].State != StateSent || rows[0].PlatformMessageID.String != "tg-42" {
		t.Fatalf("op = %s/%s", rows[0].State, rows[0].PlatformMessageID.String)
	}
	if sender.calls[0].Address.ChatKey != "chat-a" || sender.calls[0].Kind != OpSendText {
		t.Fatalf("op decoded wrong: %+v", sender.calls[0])
	}
}

func TestDispatchRespectsDependsOn(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	o0, o1 := op("d-1", 0), op("d-1", 1)
	o1.DependsOn = []int{0}
	appendOps(t, s, db, []Op{o0, o1})

	sender := &fakeSender{results: []sendResult{
		{receipt: pkgchannel.SendResult{PlatformMessageID: "m0"}},
		{receipt: pkgchannel.SendResult{PlatformMessageID: "m1"}},
	}}
	n, err := s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 1 {
		t.Fatalf("first sweep: n=%d err=%v (op1 must wait for op0)", n, err)
	}
	states := outboxStates(t, db)
	if states[0] != StateSent || states[1] != StatePending {
		t.Fatalf("states = %v", states)
	}
	n, err = s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 1 {
		t.Fatalf("second sweep: n=%d err=%v", n, err)
	}
	states = outboxStates(t, db)
	if states[0] != StateSent || states[1] != StateSent {
		t.Fatalf("states = %v", states)
	}
}

func TestDispatchClassifiesFailures(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	appendOps(t, s, db, []Op{op("d-1", 0), op("d-1", 1), op("d-1", 2)})

	sender := &fakeSender{results: []sendResult{
		{err: pkgchannel.SendErrorf(pkgchannel.SendRetryable, "rate limited")},
		{err: pkgchannel.SendErrorf(pkgchannel.SendPermanent, "chat not found")},
		{err: pkgchannel.SendErrorf(pkgchannel.SendUnknown, "eof mid-response")},
	}}
	n, err := s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 3 {
		t.Fatalf("ProcessDue: n=%d err=%v", n, err)
	}
	rows, _ := s.ListByDelivery(ctx, "d-1")
	if rows[0].State != StatePending || !rows[0].NextAttemptAt.Valid {
		t.Fatalf("retryable op = %s, want pending+rescheduled", rows[0].State)
	}
	if rows[1].State != StateFailed {
		t.Fatalf("permanent op = %s, want failed", rows[1].State)
	}
	if rows[2].State != StateUnknown {
		t.Fatalf("unknown op = %s, want unknown", rows[2].State)
	}
	// Retryable op is not due again immediately.
	n, err = s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 0 {
		t.Fatalf("rescheduled op re-claimed early: n=%d", n)
	}
	// An unknown op is never silently resent; only an explicit requeue lands.
	sender.results = []sendResult{{receipt: pkgchannel.SendResult{PlatformMessageID: "m2"}}}
	n, err = s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 0 {
		t.Fatalf("unknown op resent without requeue: n=%d", n)
	}
	tx, _ := db.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	next := time.Now().UTC()
	if ok, err := s.Requeue(ctx, tx, rows[2].ID, &next); err != nil || !ok {
		t.Fatalf("requeue: ok=%v err=%v", ok, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ProcessDue(ctx, "ch-1", "", sender); err != nil || n != 1 {
		t.Fatalf("after requeue: n=%d err=%v", n, err)
	}
	rows, _ = s.ListByDelivery(ctx, "d-1")
	if rows[2].State != StateSent {
		t.Fatalf("requeued op = %s", rows[2].State)
	}
}

func TestDispatchUndecodableOpFails(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()
	bad := op("d-1", 0)
	bad.Address = []byte(`{"chat_key":123}`)
	appendOps(t, s, db, []Op{bad})

	sender := &fakeSender{}
	n, err := s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(sender.calls) != 0 {
		t.Fatal("undecodable op reached the adapter")
	}
	rows, _ := s.ListByDelivery(ctx, "d-1")
	if rows[0].State != StateFailed {
		t.Fatalf("op = %s, want failed", rows[0].State)
	}
}

func TestDispatchSplitReplyRetriesOnlyFailedChunk(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	// Three-chunk reply; the production splitter is ReplyOps with a limit.
	ops, err := ReplyOps("", "d-1", "ch-1", "bot-1",
		Address{V: AddressVersion, ChatKey: "chat-a"}, "aaaa bbbb cccc", 5)
	if err != nil || len(ops) != 3 {
		t.Fatalf("split: %d ops err=%v", len(ops), err)
	}
	appendOps(t, s, db, ops)

	sender := &fakeSender{results: []sendResult{
		{receipt: pkgchannel.SendResult{PlatformMessageID: "m0"}},
		{err: pkgchannel.SendErrorf(pkgchannel.SendRetryable, "429")},
	}}
	n, err := s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 1 {
		// op0 sent; op1/op2 are not due while their dependency is unsent.
		t.Fatalf("first sweep: n=%d err=%v", n, err)
	}
	n, err = s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 1 {
		// op1 attempted and failed; op2 still blocked behind it.
		t.Fatalf("second sweep: n=%d err=%v", n, err)
	}
	states := outboxStates(t, db)
	if states[0] != StateSent || states[1] != StatePending || states[2] != StatePending {
		t.Fatalf("states = %v", states)
	}

	// Reschedule op1 into the past and dispatch: only op1+op2 send; op0's
	// confirmed receipt is never replayed.
	if _, err := db.Exec(ctx, "UPDATE channel_outbox SET next_attempt_at = clock_timestamp() WHERE state='pending' AND operation_index=1"); err != nil {
		t.Fatal(err)
	}
	sender.results = []sendResult{
		{receipt: pkgchannel.SendResult{PlatformMessageID: "m1"}},
		{receipt: pkgchannel.SendResult{PlatformMessageID: "m2"}},
	}
	n, err = s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 1 {
		// op1 resent; op2 unlocks only after op1's receipt commits.
		t.Fatalf("third sweep: n=%d err=%v", n, err)
	}
	n, err = s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 1 {
		t.Fatalf("fourth sweep: n=%d err=%v", n, err)
	}
	if len(sender.calls) != 4 {
		t.Fatalf("calls = %d, want 4 total", len(sender.calls))
	}
	if sender.calls[2].OperationIndex != 1 || sender.calls[3].OperationIndex != 2 {
		t.Fatalf("resent indexes = %d,%d want 1,2", sender.calls[2].OperationIndex, sender.calls[3].OperationIndex)
	}
	states = outboxStates(t, db)
	for i, st := range states {
		if st != StateSent {
			t.Fatalf("op %d = %s", i, st)
		}
	}
}

// A notify op skips the source-account fence — the notification has no
// triggering account, so the channel's current bot identity is the sender.
func TestDispatchNotifySkipsAccountFence(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	payload, _ := json.Marshal(NotifyPayload{V: PayloadVersion, Notification: pkgchannel.Notification{ChatID: "chat-a", Text: "hi"}})
	addr, _ := json.Marshal(Address{V: AddressVersion, ChatKey: "chat-a"})
	appendOps(t, s, db, []Op{{DeliveryKey: "notify:1", Index: 0, Kind: OpNotify, ChannelID: "ch-1", AccountKey: "ch-1", Address: addr, Payload: payload}})

	sender := &fakeSender{results: []sendResult{{receipt: pkgchannel.SendResult{PlatformMessageID: "n-1"}}}}
	n, err := s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 1 {
		t.Fatalf("ProcessDue: n=%d err=%v", n, err)
	}
	if sender.calls[0].Kind != OpNotify {
		t.Fatalf("op kind = %s", sender.calls[0].Kind)
	}
	rows, _ := s.ListByDelivery(ctx, "notify:1")
	if rows[0].State != StateSent {
		t.Fatalf("notify op = %s, want sent", rows[0].State)
	}
}
