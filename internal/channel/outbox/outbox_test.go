package outbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
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

// A long notification decomposes into one op per segment, chained so a
// retried segment is never overtaken; each segment is one platform call.
func TestNotifyChainSplitsAndChains(t *testing.T) {
	n := pkgchannel.Notification{ChatID: "chat-9", Text: strings.Repeat("x", 25), Silent: true}
	ops, err := NotifyChain("notify:d-1", "ch-1", "bot-1", n, 10)
	if err != nil {
		t.Fatalf("NotifyChain: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("ops = %d, want 3 segments", len(ops))
	}
	for i, o := range ops {
		if o.Kind != OpNotify {
			t.Fatalf("op %d kind = %s", i, o.Kind)
		}
		var p NotifyPayload
		if err := json.Unmarshal(o.Payload, &p); err != nil {
			t.Fatalf("op %d payload: %v", i, err)
		}
		if len(p.Notification.Text) > 10 || !p.Notification.Silent {
			t.Fatalf("op %d segment = %q silent=%v", i, p.Notification.Text, p.Notification.Silent)
		}
		wantDeps := 0
		if i > 0 {
			wantDeps = 1
		}
		if len(o.DependsOn) != wantDeps {
			t.Fatalf("op %d deps = %v", i, o.DependsOn)
		}
	}
}

// A platform without binary upload folds media into text markers instead of
// attachment ops, so every group op stays one platform call.
func TestGroupReplyChainFoldsMediaAsText(t *testing.T) {
	events := []pkgchannel.Event{
		{Text: "answer"},
		{Image: &pkgchannel.ImageEvent{Data: "AA==", MimeType: "image/png"}},
	}
	payload := GroupReplyPayload{
		V: PayloadVersion, Platform: "qq",
		PlatformGroupID: "g1", DeliveryID: "d-1",
	}
	ops, err := GroupReplyChain("group:d-1", "ch-1", "bot-1", "", payload, events,
		ReplyPlan{TextLimit: 3500, PrimaryText: true, MediaAsText: true})
	if err != nil {
		t.Fatalf("GroupReplyChain: %v", err)
	}
	if len(ops) != 1 || ops[0].Kind != OpSendGroupReply {
		t.Fatalf("ops = %v, want one send_group_reply", ops)
	}
	var p pkgchannel.GroupReplyOpPayload
	if err := json.Unmarshal(ops[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Text, "[Image: image/png]") {
		t.Fatalf("primary text missing media marker: %q", p.Text)
	}
}

// A redelivered append carries the same JSON under a different serialization —
// key order and whitespace — and must dedup on JSONB semantics, not bytes.
func TestAppendDedupMatchesJSONBSemantically(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	first := op("d-1", 0)
	first.Payload = json.RawMessage(`{"v":1,"text":"hi"}`)
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, tx, []Op{first}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	again := first
	again.Payload = json.RawMessage(`{ "text":"hi", "v":1 }`)
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, tx, []Op{again}); err != nil {
		t.Fatalf("reordered payload rejected: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListByDelivery(ctx, "d-1")
	if len(rows) != 1 {
		t.Fatalf("semantic dedup duplicated the op: %d rows", len(rows))
	}
}

// Same delivery key and index but a different frozen identity — here the
// payload body — is a real conflict, never a silent merge.
func TestAppendDedupRejectsChangedIdentity(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, tx, []Op{op("d-1", 0)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	changed := op("d-1", 0)
	changed.Payload = json.RawMessage(`{"v":1,"text":"different"}`)
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, tx, []Op{changed}); err == nil {
		t.Fatal("changed payload deduped silently")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

// Attachment presence is nil vs non-nil: a zero-byte file is a real body that
// must commit and dedup; a body where none was requested — or different bytes
// where one was — is a mismatch.
func TestAppendAttachmentDedup(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	empty := op("d-1", 0)
	empty.Kind = OpSendAttachment
	empty.Attachment = []byte{} // zero-byte file: non-nil presence
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, tx, []Op{empty}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM channel_outbox_attachment a
		 JOIN channel_outbox o ON o.id = a.outbox_id
		 WHERE o.delivery_key = 'd-1' AND a.data = ''::bytea`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("zero-byte attachment not stored: n=%d err=%v", n, err)
	}

	// Same empty body re-appends cleanly.
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, tx, []Op{empty}); err != nil {
		t.Fatalf("zero-byte dedup: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// No attachment where one was stored: mismatch, not a silent skip.
	bare := empty
	bare.Attachment = nil
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, tx, []Op{bare}); err == nil {
		t.Fatal("attachment dropped on redelivery deduped silently")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// Different bytes under the same key: mismatch.
	other := empty
	other.Attachment = []byte("not empty")
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, tx, []Op{other}); err == nil {
		t.Fatal("different attachment bytes deduped silently")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}
