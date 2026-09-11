package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
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

func receiveParams(channelID, eventKey, chatKey string) ReceiveParams {
	return ReceiveParams{
		ChannelID:        channelID,
		SourceAccountKey: "bot-1",
		EventKey:         eventKey,
		EventKind:        KindMessage,
		PayloadVersion:   1,
		Payload:          json.RawMessage(`{"text":"hi"}`),
		ChatKey:          chatKey,
		Ready:            true,
	}
}

func TestReceiveDeduplicatesRedelivery(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)

	const n = 8
	var wg sync.WaitGroup
	created := make([]bool, n)
	ids := make([]string, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			row, c, err := s.Receive(t.Context(), receiveParams("ch-1", "evt-1", "chat-a"))
			created[i], errs[i] = c, err
			if err == nil {
				ids[i] = row.ID
			}
		}(i)
	}
	wg.Wait()

	wins := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("receive %d: %v", i, errs[i])
		}
		if created[i] {
			wins++
		}
		if ids[i] == "" || ids[i] != ids[0] {
			t.Fatalf("redelivery returned different row: %q vs %q", ids[i], ids[0])
		}
	}
	if wins != 1 {
		t.Fatalf("expected exactly one created row, got %d", wins)
	}
}

func TestReceiveAssignsContiguousIngressSeq(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)

	const n = 8
	seqs := make(chan int64, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := receiveParams("ch-1", "evt-"+string(rune('a'+i)), "chat-a")
			row, created, err := s.Receive(t.Context(), p)
			if err != nil {
				t.Errorf("receive: %v", err)
				return
			}
			if !created {
				t.Errorf("event %d not created", i)
				return
			}
			seqs <- row.IngressSeq
		}(i)
	}
	wg.Wait()
	close(seqs)

	got := make([]int64, 0, n)
	for s := range seqs {
		got = append(got, s)
	}
	slices.Sort(got)
	for i, v := range got {
		if v != int64(i+1) {
			t.Fatalf("ingress_seq not contiguous 1..n: %v", got)
		}
	}
}

func TestReceiveRefusesMissingIdentity(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)

	p := receiveParams("ch-1", "", "chat-a")
	if _, _, err := s.Receive(t.Context(), p); err == nil {
		t.Fatal("empty event key accepted")
	}
	p = receiveParams("ch-1", "evt-1", "chat-a")
	p.SourceAccountKey = ""
	if _, _, err := s.Receive(t.Context(), p); err == nil {
		t.Fatal("empty account key accepted")
	}
	if _, _, err := s.Receive(t.Context(), receiveParams("ch-gone", "evt-1", "chat-a")); !errors.Is(err, ErrChannelGone) {
		t.Fatalf("deleted channel: got %v, want ErrChannelGone", err)
	}
}

func TestListPendingOrdersPerChatAndSkipsDeferred(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	for _, e := range []struct{ key, chat string }{
		{"e1", "chat-b"}, {"e2", "chat-a"}, {"e3", "chat-b"},
	} {
		if _, _, err := s.Receive(ctx, receiveParams("ch-1", e.key, e.chat)); err != nil {
			t.Fatalf("receive %s: %v", e.key, err)
		}
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := s.ListPending(ctx, tx, "ch-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 pending, got %d", len(rows))
	}
	// chat_key first, then ingress_seq inside each chat.
	wantKeys := []string{"e2", "e1", "e3"}
	for i, r := range rows {
		if r.EventKey != wantKeys[i] {
			t.Fatalf("order %d: want %s, got %s", i, wantKeys[i], r.EventKey)
		}
	}

	// A deferred row stays out of the scan until its time comes.
	later := time.Now().UTC().Add(time.Hour)
	if ok, err := s.Transition(ctx, tx, rows[1].ID, StateReceived, "", &later); err != nil || !ok {
		t.Fatalf("defer: ok=%v err=%v", ok, err)
	}
	rows, err = s.ListPending(ctx, tx, "ch-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("deferred row still pending-listed: %d rows", len(rows))
	}
}

func TestMarkRoutedIsOneWay(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	row, _, err := s.Receive(ctx, receiveParams("ch-1", "e1", "chat-a"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if ok, err := s.MarkRouted(ctx, tx, row.ID); err != nil || !ok {
		t.Fatalf("mark routed: ok=%v err=%v", ok, err)
	}
	// A routed event cannot be re-routed or dragged back.
	if ok, err := s.MarkRouted(ctx, tx, row.ID); err != nil || ok {
		t.Fatalf("re-route should fail: ok=%v err=%v", ok, err)
	}
	if ok, err := s.Transition(ctx, tx, row.ID, StateReady, "", nil); err != nil || ok {
		t.Fatalf("routed->ready should fail: ok=%v err=%v", ok, err)
	}
}
