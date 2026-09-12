package telegram

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tele "gopkg.in/telebot.v4"

	choutbox "github.com/CherryHQ/stella/internal/channel/outbox"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/channel"
)

var tgReplyPlan = choutbox.ReplyPlan{TextLimit: telegramMaxMessageLen, PrimaryText: true, Attachments: true}

var tgAddress = choutbox.Address{V: choutbox.AddressVersion, ChatKey: "-100", ReplyToKey: "55"}

func createOutboxChannel(t *testing.T, db *pgxpool.Pool, id, ownerToken string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(ctx,
		`INSERT INTO channel (id, name, type, enabled, config) VALUES ($1, $1, 'telegram', true, '{}')`, id); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if ownerToken != "" {
		if _, err := db.Exec(ctx,
			`UPDATE channel SET runtime_token = $2::uuid,
				runtime_lease_until = clock_timestamp() + interval '1 hour'
			WHERE id = $1`, id, ownerToken); err != nil {
			t.Fatalf("set owner lease: %v", err)
		}
	}
}

func appendOps(t *testing.T, db *pgxpool.Pool, s *choutbox.Store, ops []choutbox.Op) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := s.Append(ctx, tx, ops); err != nil {
		t.Fatalf("append ops: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func opState(t *testing.T, db *pgxpool.Pool, deliveryKey string, index int) string {
	t.Helper()
	var state string
	if err := db.QueryRow(context.Background(),
		`SELECT state FROM channel_outbox WHERE delivery_key = $1 AND operation_index = $2`,
		deliveryKey, index).Scan(&state); err != nil {
		t.Fatalf("query op state: %v", err)
	}
	return state
}

func forceDue(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	if _, err := db.Exec(context.Background(),
		`UPDATE channel_outbox SET next_attempt_at = clock_timestamp() - interval '1 second' WHERE state = 'pending'`); err != nil {
		t.Fatalf("force due: %v", err)
	}
}

func fakeImageEvent() channel.Event {
	return channel.Event{Image: &channel.ImageEvent{
		Data:     base64.StdEncoding.EncodeToString([]byte("png-bytes")),
		MimeType: "image/png",
	}}
}

func fileEvent(t *testing.T) channel.Event {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("file-bytes"), 0o600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	return channel.Event{File: &channel.FileEvent{Name: "report.txt", Path: path}}
}

// When a later reply segment fails retryably, the segment that already
// delivered must not be replayed: each message call gets its own durable op.
func TestSendReplyChainResumesOnlyFailedSegment(t *testing.T) {
	db := dbtest.New(t)
	createOutboxChannel(t, db, "ch-tg-reply", "")
	s := choutbox.New(db)
	fake := &telegramAPIFake{responses: map[string][]string{}}
	b := newPublisherTestBot(t, fake)
	b.bot.Me = &tele.User{ID: 1, Username: "bot-1"}
	ctx := context.Background()

	longText := strings.Repeat("word ", 900) // 4500 chars -> two 4000-limit segments
	ops, err := choutbox.ReplyChain("", "run:seg", "ch-tg-reply", "bot-1",
		tgAddress, "sess", []channel.Event{{Text: longText}}, tgReplyPlan)
	if err != nil {
		t.Fatalf("ReplyChain: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops for 2 segments, got %d", len(ops))
	}
	appendOps(t, db, s, ops)

	// The chain is dependency-ordered: op 1 becomes due only after op 0's
	// receipt is recorded, so each sweep below moves one link forward.
	fake.responses["sendMessage"] = []string{
		`{"ok":true,"result":{"message_id":1}}`,
		`{"ok":false,"error_code":429,"description":"flood control","parameters":{"retry_after":0}}`,
	}
	if _, err := s.ProcessDue(ctx, "ch-tg-reply", "", b); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if got := opState(t, db, "run:seg", 0); got != "sent" {
		t.Fatalf("first segment op = %s, want sent", got)
	}
	if calls := len(fake.callsFor("sendMessage")); calls != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", calls)
	}
	if _, err := s.ProcessDue(ctx, "ch-tg-reply", "", b); err != nil {
		t.Fatalf("ProcessDue segment 2: %v", err)
	}
	if got := opState(t, db, "run:seg", 1); got != "pending" {
		t.Fatalf("second segment op = %s, want pending after 429", got)
	}
	if calls := len(fake.callsFor("sendMessage")); calls != 2 {
		t.Fatalf("sendMessage calls = %d, want 2", calls)
	}

	forceDue(t, db)
	if _, err := s.ProcessDue(ctx, "ch-tg-reply", "", b); err != nil {
		t.Fatalf("ProcessDue retry: %v", err)
	}

	if got := opState(t, db, "run:seg", 1); got != "sent" {
		t.Fatalf("second segment op = %s, want sent after retry", got)
	}
	calls := fake.callsFor("sendMessage")
	if len(calls) != 3 {
		t.Fatalf("sendMessage calls = %d, want 3 (no resend of segment 1)", len(calls))
	}
	// The retried call must be segment 2 only: segment 1 text never resends.
	seg0, seg1, retry := calls[0].params["text"], calls[1].params["text"], calls[2].params["text"]
	if retry != seg1 || retry == seg0 {
		t.Fatalf("retry resent the wrong segment: %v vs %v / %v", retry, seg0, seg1)
	}
	// Each external call got its own durable receipt.
	var receipts int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM channel_outbox WHERE delivery_key = 'run:seg' AND state = 'sent' AND platform_message_id IS NOT NULL`).Scan(&receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if receipts != 2 {
		t.Fatalf("durable receipts = %d, want 2", receipts)
	}
}

// Attachments split into independent ops: a failed file send retries alone and
// the already-landed image is not replayed.
func TestSendReplyPartialAttachmentFailure(t *testing.T) {
	db := dbtest.New(t)
	createOutboxChannel(t, db, "ch-tg-att", "")
	s := choutbox.New(db)
	fake := &telegramAPIFake{responses: map[string][]string{}}
	b := newPublisherTestBot(t, fake)
	b.bot.Me = &tele.User{ID: 1, Username: "bot-1"}
	ctx := context.Background()

	events := []channel.Event{{Text: "done"}, fakeImageEvent(), fileEvent(t)}
	ops, err := choutbox.ReplyChain("", "run:att", "ch-tg-att", "bot-1",
		tgAddress, "sess", events, tgReplyPlan)
	if err != nil {
		t.Fatalf("ReplyChain: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("expected reply+image+file ops, got %d", len(ops))
	}
	appendOps(t, db, s, ops)

	fake.responses["sendPhoto"] = []string{
		`{"ok":true,"result":{"message_id":10,"photo":[{"file_id":"p1","file_unique_id":"u1","width":1,"height":1}]}}`,
	}
	fake.responses["sendDocument"] = []string{
		`{"ok":false,"error_code":429,"description":"flood control","parameters":{"retry_after":0}}`,
		`{"ok":true,"result":{"message_id":11,"document":{"file_id":"d1","file_unique_id":"u2","file_name":"report.txt"}}}`,
	}
	// Three dependency-ordered sweeps: reply, image, then the failed file.
	for i := range 3 {
		if _, err := s.ProcessDue(ctx, "ch-tg-att", "", b); err != nil {
			t.Fatalf("ProcessDue %d: %v", i, err)
		}
	}
	if calls := len(fake.callsFor("sendPhoto")); calls != 1 {
		t.Fatalf("sendPhoto calls = %d, want 1", calls)
	}
	if got := opState(t, db, "run:att", 2); got != "pending" {
		t.Fatalf("file op = %s, want pending after 429", got)
	}

	forceDue(t, db)
	if _, err := s.ProcessDue(ctx, "ch-tg-att", "", b); err != nil {
		t.Fatalf("ProcessDue retry: %v", err)
	}

	if calls := len(fake.callsFor("sendPhoto")); calls != 1 {
		t.Fatalf("sendPhoto calls = %d after retry, want 1 (landed image not resent)", calls)
	}
	if calls := len(fake.callsFor("sendDocument")); calls != 2 {
		t.Fatalf("sendDocument calls = %d, want 2", calls)
	}
	if got := opState(t, db, "run:att", 2); got != "sent" {
		t.Fatalf("file op = %s, want sent", got)
	}
}

// Owner-admission is re-validated before every external call in the claimed
// batch: once the lease token rotates, later ops never reach the SDK. Two
// independent deliveries land in the same claim batch because only ops
// without dependencies are due together.
func TestOwnershipLossBlocksLaterCallsInBatch(t *testing.T) {
	db := dbtest.New(t)
	createOutboxChannel(t, db, "ch-tg-own", "00000000-0000-0000-0000-0000000000aa")
	s := choutbox.New(db)
	fake := &telegramAPIFake{responses: map[string][]string{}}
	b := newPublisherTestBot(t, fake)
	b.bot.Me = &tele.User{ID: 1, Username: "bot-1"}
	ctx := context.Background()

	for _, key := range []string{"msg:a", "msg:b"} {
		ops, err := choutbox.ReplyOps("", key, "ch-tg-own", "bot-1", tgAddress, "hello "+key, 0)
		if err != nil {
			t.Fatalf("ReplyOps: %v", err)
		}
		appendOps(t, db, s, ops)
	}

	// The first send succeeds, then the lease token rotates before the next
	// op's admission check.
	flip := &tokenFlipSender{inner: b, db: db, channelID: "ch-tg-own"}
	if _, err := s.ProcessDue(ctx, "ch-tg-own", "00000000-0000-0000-0000-0000000000aa", flip); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	if calls := len(fake.callsFor("sendMessage")); calls != 1 {
		t.Fatalf("sendMessage calls = %d, want 1 (later ops must not reach SDK)", calls)
	}
	if got := opState(t, db, "msg:b", 0); got != "sending" {
		t.Fatalf("second op = %s, want sending (claimed but not delivered)", got)
	}
	// The stale claim must never blindly resend: expiry lands it at unknown.
	if _, err := db.Exec(ctx,
		`UPDATE channel_outbox SET attempt_started_at = clock_timestamp() - interval '10 minutes'
		 WHERE delivery_key = 'msg:b' AND operation_index = 0`); err != nil {
		t.Fatalf("age attempt: %v", err)
	}
	if n, err := s.ExpireAttempts(ctx); err != nil || n == 0 {
		t.Fatalf("expected stale claim to expire, n=%d err=%v", n, err)
	}
	if got := opState(t, db, "msg:b", 0); got != "unknown" {
		t.Fatalf("stale claim = %s, want unknown", got)
	}
	// Restore ownership: the unknown op stays untouched — no blind resend.
	if _, err := db.Exec(ctx,
		`UPDATE channel SET runtime_token = '00000000-0000-0000-0000-0000000000aa'::uuid WHERE id = 'ch-tg-own'`); err != nil {
		t.Fatalf("restore token: %v", err)
	}
	if _, err := s.ProcessDue(ctx, "ch-tg-own", "00000000-0000-0000-0000-0000000000aa", b); err != nil {
		t.Fatalf("ProcessDue after expiry: %v", err)
	}
	if calls := len(fake.callsFor("sendMessage")); calls != 1 {
		t.Fatalf("sendMessage calls = %d after expiry, want 1 (unknown never resent)", calls)
	}
}

// A batch claimed under a rotated token is refused before any SDK call.
func TestOwnershipLossBlocksWholeBatch(t *testing.T) {
	db := dbtest.New(t)
	createOutboxChannel(t, db, "ch-tg-gone", "00000000-0000-0000-0000-0000000000bb")
	s := choutbox.New(db)
	fake := &telegramAPIFake{responses: map[string][]string{}}
	b := newPublisherTestBot(t, fake)
	b.bot.Me = &tele.User{ID: 1, Username: "bot-1"}
	ctx := context.Background()

	ops, err := choutbox.ReplyChain("", "run:gone", "ch-tg-gone", "bot-1",
		tgAddress, "sess", []channel.Event{{Text: "hello"}}, tgReplyPlan)
	if err != nil {
		t.Fatalf("ReplyChain: %v", err)
	}
	appendOps(t, db, s, ops)

	n, err := s.ProcessDue(ctx, "ch-tg-gone", "00000000-0000-0000-0000-0000000000cc", b)
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if n != 0 {
		t.Fatalf("stale token claimed %d ops, want 0", n)
	}
	if calls := len(fake.callsFor("sendMessage")); calls != 0 {
		t.Fatalf("sendMessage calls = %d, want 0", calls)
	}
	if got := opState(t, db, "run:gone", 0); got != "pending" {
		t.Fatalf("op = %s, want pending", got)
	}
}

type tokenFlipSender struct {
	inner     channel.OperationSender
	db        *pgxpool.Pool
	channelID string
	flipped   bool
}

func (f *tokenFlipSender) SendOperation(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	res, err := f.inner.SendOperation(ctx, op)
	if !f.flipped {
		f.flipped = true
		if _, derr := f.db.Exec(ctx,
			`UPDATE channel SET runtime_token = '00000000-0000-0000-0000-0000000000ff'::uuid WHERE id = $1`, f.channelID); derr != nil {
			return res, fmt.Errorf("flip token: %w", derr)
		}
	}
	return res, err
}
