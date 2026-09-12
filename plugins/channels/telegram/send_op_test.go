package telegram

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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

func fileEvent() channel.Event {
	return channel.Event{File: &channel.FileEvent{
		Name: "report.txt",
		Data: []byte("file-bytes"),
	}}
}

// stubAttachmentOpener reads the real channel_outbox_attachment row like the
// production surface; onOpen runs inside the read so a test can flip the
// lease mid-fetch.
type stubAttachmentOpener struct {
	fakeChannelHandler
	db     *pgxpool.Pool
	onOpen func()
}

func (h stubAttachmentOpener) OpenAttachment(ctx context.Context, outboxID string) ([]byte, error) {
	if h.onOpen != nil {
		h.onOpen()
	}
	var data []byte
	err := h.db.QueryRow(ctx, `SELECT data FROM channel_outbox_attachment WHERE outbox_id = $1`, outboxID).Scan(&data)
	return data, err
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
	b.handler = stubAttachmentOpener{db: db}
	ctx := context.Background()

	// A frozen topic/reply anchor must reach the document send too, not only
	// the text op — an empty SendOptions would drop both coordinates.
	address := choutbox.Address{V: choutbox.AddressVersion, ChatKey: "-100", ThreadKey: "42", ReplyToKey: "55"}
	events := []channel.Event{{Text: "done"}, fakeImageEvent(), fileEvent()}
	ops, err := choutbox.ReplyChain("", "run:att", "ch-tg-att", "bot-1",
		address, "sess", events, tgReplyPlan)
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
	docs := fake.callsFor("sendDocument")
	if len(docs) != 2 {
		t.Fatalf("sendDocument calls = %d, want 2", len(docs))
	}
	if got := docs[0].params["message_thread_id"]; got != "42" {
		t.Fatalf("attachment thread = %#v, want the frozen 42", got)
	}
	if got := docs[0].params["reply_to_message_id"]; got != "55" {
		t.Fatalf("attachment reply anchor = %#v, want the frozen 55", got)
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

// A markdown rejection triggers the plain-text fallback — a second SDK call
// inside the same op. When the lease moves between the two, the guard must
// refuse the fallback; while owned, the fallback proceeds.
func TestMarkdownFallbackBlockedByOwnershipLoss(t *testing.T) {
	db := dbtest.New(t)
	createOutboxChannel(t, db, "ch-tg-fb", "00000000-0000-0000-0000-0000000000dd")
	s := choutbox.New(db)
	fake := &telegramAPIFake{responses: map[string][]string{}}
	b := newPublisherTestBot(t, fake)
	b.bot.Me = &tele.User{ID: 1, Username: "bot-1"}
	ctx := context.Background()

	ops, err := choutbox.ReplyOps("", "d-fb", "ch-tg-fb", "bot-1", tgAddress, "hello", 0)
	if err != nil {
		t.Fatalf("ReplyOps: %v", err)
	}
	appendOps(t, db, s, ops)

	// Markdown send is rejected with a platform 400; the lease rotates before
	// the op can issue the plain-text retry.
	fake.responses["sendMessage"] = []string{
		`{"ok":false,"error_code":400,"description":"can't parse entities"}`,
	}
	var once sync.Once
	fake.onCall = func(method string) {
		once.Do(func() {
			if _, err := db.Exec(ctx,
				`UPDATE channel SET runtime_token = '00000000-0000-0000-0000-0000000000ee'::uuid WHERE id = 'ch-tg-fb'`); err != nil {
				t.Errorf("flip token: %v", err)
			}
		})
	}
	if _, err := s.ProcessDue(ctx, "ch-tg-fb", "00000000-0000-0000-0000-0000000000dd", b); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if calls := len(fake.callsFor("sendMessage")); calls != 1 {
		t.Fatalf("sendMessage calls = %d, want 1 — fenced-out plain fallback must not fire", calls)
	}
	if got := opState(t, db, "d-fb", 0); got != "pending" {
		t.Fatalf("op = %s, want pending (ownership loss is retryable)", got)
	}
}

// Control: while the lease holds, a markdown 400 falls back to a plain send
// and the op completes with a receipt.
func TestMarkdownFallbackSendsWhileOwned(t *testing.T) {
	db := dbtest.New(t)
	createOutboxChannel(t, db, "ch-tg-ok", "00000000-0000-0000-0000-0000000000dd")
	s := choutbox.New(db)
	fake := &telegramAPIFake{responses: map[string][]string{}}
	b := newPublisherTestBot(t, fake)
	b.bot.Me = &tele.User{ID: 1, Username: "bot-1"}
	ctx := context.Background()

	ops, err := choutbox.ReplyOps("", "d-ok", "ch-tg-ok", "bot-1", tgAddress, "hello", 0)
	if err != nil {
		t.Fatalf("ReplyOps: %v", err)
	}
	appendOps(t, db, s, ops)
	fake.responses["sendMessage"] = []string{
		`{"ok":false,"error_code":400,"description":"can't parse entities"}`,
		`{"ok":true,"result":{"message_id":7}}`,
	}
	if _, err := s.ProcessDue(ctx, "ch-tg-ok", "00000000-0000-0000-0000-0000000000dd", b); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if calls := len(fake.callsFor("sendMessage")); calls != 2 {
		t.Fatalf("sendMessage calls = %d, want markdown+plain", calls)
	}
	if got := opState(t, db, "d-ok", 0); got != "sent" {
		t.Fatalf("op = %s, want sent", got)
	}
}

// An accepted group reply decomposes into one durable op per platform call:
// primary segment, overflow text, each attachment — each with its own
// receipt, and a mid-chain failure resends only the failed op.
func TestGroupReplyChainDeliversPerCall(t *testing.T) {
	db := dbtest.New(t)
	createOutboxChannel(t, db, "ch-tg-grp", "")
	s := choutbox.New(db)
	fake := &telegramAPIFake{responses: map[string][]string{}}
	b := newPublisherTestBot(t, fake)
	b.bot.Me = &tele.User{ID: 1, Username: "bot-1"}
	ctx := context.Background()

	longText := strings.Repeat("word ", 900) // two 4000-limit segments
	events := []channel.Event{{Text: longText}, fakeImageEvent()}
	payload := choutbox.GroupReplyPayload{
		V: choutbox.PayloadVersion, Platform: "telegram",
		PlatformGroupID: "-100", PlatformThreadID: "42", ReplyTo: "7",
		DeliveryID: "d-grp", Events: events,
	}
	ops, err := choutbox.GroupReplyChain("group:d-grp", "ch-tg-grp", "bot-1", "", payload, events, tgReplyPlan)
	if err != nil {
		t.Fatalf("GroupReplyChain: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("expected reply+text+image ops, got %d", len(ops))
	}
	if ops[0].Kind != "send_group_reply" || ops[1].Kind != "send_text" || ops[2].Kind != "send_attachment" {
		t.Fatalf("kinds = %s,%s,%s", ops[0].Kind, ops[1].Kind, ops[2].Kind)
	}
	appendOps(t, db, s, ops)

	// Sweep 1: primary group send lands, anchored to topic 42 replying to 7.
	fake.responses["sendMessage"] = []string{
		`{"ok":true,"result":{"message_id":20}}`,
		`{"ok":false,"error_code":429,"description":"flood","parameters":{"retry_after":0}}`,
		`{"ok":true,"result":{"message_id":21}}`,
	}
	fake.responses["sendPhoto"] = []string{
		`{"ok":true,"result":{"message_id":22,"photo":[{"file_id":"p1","file_unique_id":"u1","width":1,"height":1}]}}`,
	}
	if _, err := s.ProcessDue(ctx, "ch-tg-grp", "", b); err != nil {
		t.Fatalf("sweep 1: %v", err)
	}
	if got := opState(t, db, "group:d-grp", 0); got != "sent" {
		t.Fatalf("primary op = %s, want sent", got)
	}
	sends := fake.callsFor("sendMessage")
	if sends[0].params["message_thread_id"] != "42" {
		t.Fatalf("group send thread = %#v, want 42", sends[0].params["message_thread_id"])
	}
	// Sweep 2: overflow send_text hits 429 — only it stays pending.
	if _, err := s.ProcessDue(ctx, "ch-tg-grp", "", b); err != nil {
		t.Fatalf("sweep 2: %v", err)
	}
	if got := opState(t, db, "group:d-grp", 1); got != "pending" {
		t.Fatalf("overflow op = %s, want pending after 429", got)
	}
	forceDue(t, db)
	if _, err := s.ProcessDue(ctx, "ch-tg-grp", "", b); err != nil {
		t.Fatalf("sweep 3: %v", err)
	}
	if _, err := s.ProcessDue(ctx, "ch-tg-grp", "", b); err != nil {
		t.Fatalf("sweep 4: %v", err)
	}
	// The landed primary is never resent; the attachment delivered once.
	sends = fake.callsFor("sendMessage")
	if len(sends) != 3 {
		t.Fatalf("sendMessage calls = %d, want 3 (primary + failed + retry)", len(sends))
	}
	if calls := len(fake.callsFor("sendPhoto")); calls != 1 {
		t.Fatalf("sendPhoto calls = %d, want 1", calls)
	}
	for i := range 3 {
		if got := opState(t, db, "group:d-grp", i); got != "sent" {
			t.Fatalf("op %d = %s, want sent", i, got)
		}
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

// A send_group_reply enqueued under bot-1 must not go out after the channel
// answers to bot-2: the account fence fails the op before any API call, and
// its dependents cancel rather than inherit a dead chain.
func TestGroupReplyOpRejectsReboundAccount(t *testing.T) {
	db := dbtest.New(t)
	createOutboxChannel(t, db, "ch-tg-grp2", "")
	s := choutbox.New(db)
	fake := &telegramAPIFake{responses: map[string][]string{}}
	b := newPublisherTestBot(t, fake)
	b.bot.Me = &tele.User{ID: 1, Username: "bot-2"} // rebound: this bot is not bot-1
	ctx := context.Background()

	events := []channel.Event{{Text: strings.Repeat("word ", 900)}, fakeImageEvent()}
	payload := choutbox.GroupReplyPayload{
		V: choutbox.PayloadVersion, Platform: "telegram",
		PlatformGroupID: "-100", DeliveryID: "d-grp2", Events: events,
	}
	ops, err := choutbox.GroupReplyChain("group:d-grp2", "ch-tg-grp2", "bot-1", "", payload, events, tgReplyPlan)
	if err != nil {
		t.Fatalf("GroupReplyChain: %v", err)
	}
	appendOps(t, db, s, ops)

	for range 6 {
		if _, err := s.ProcessDue(ctx, "ch-tg-grp2", "", b); err != nil {
			t.Fatalf("ProcessDue: %v", err)
		}
	}
	if got := opState(t, db, "group:d-grp2", 0); got != "failed" {
		t.Fatalf("primary op = %s, want failed (account_mismatch)", got)
	}
	for i := 1; i < len(ops); i++ {
		if got := opState(t, db, "group:d-grp2", i); got != "canceled" {
			t.Fatalf("op %d = %s, want canceled", i, got)
		}
	}
	if len(fake.calls) != 0 {
		t.Fatalf("platform calls = %d, want 0 under a rebound account", len(fake.calls))
	}
}

// Credential rotation keeps the same account identity: a channel row rebound
// to a fresh token but still answering as bot-1 owns pending ops enqueued
// under bot-1 — the fence compares identity, not credentials.
func TestGroupReplyOpDeliversAfterCredentialRotation(t *testing.T) {
	db := dbtest.New(t)
	createOutboxChannel(t, db, "ch-tg-rot", "")
	s := choutbox.New(db)
	fake := &telegramAPIFake{responses: map[string][]string{}}
	b := newPublisherTestBot(t, fake)
	b.bot.Me = &tele.User{ID: 1, Username: "bot-1"}
	ctx := context.Background()

	payload := choutbox.GroupReplyPayload{
		V: choutbox.PayloadVersion, Platform: "telegram",
		PlatformGroupID: "-100", DeliveryID: "d-rot", Events: []channel.Event{{Text: "ok"}},
	}
	ops, err := choutbox.GroupReplyChain("group:d-rot", "ch-tg-rot", "bot-1", "", payload, []channel.Event{{Text: "ok"}}, tgReplyPlan)
	if err != nil {
		t.Fatalf("GroupReplyChain: %v", err)
	}
	appendOps(t, db, s, ops)
	if _, err := s.ProcessDue(ctx, "ch-tg-rot", "", b); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if got := opState(t, db, "group:d-rot", 0); got != "sent" {
		t.Fatalf("op = %s, want sent", got)
	}
	if calls := len(fake.callsFor("sendMessage")); calls != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", calls)
	}
}

// Reactions are platform calls too: once the lease is gone the adapter must
// not fire the ack reaction, the send, or the terminal reaction. Guard checks
// bracket each one.
func TestGroupReplyReactionsRespectOwnership(t *testing.T) {
	groupOp := func(guard func(context.Context) error) channel.OutboundOp {
		payload, err := json.Marshal(channel.GroupReplyOpPayload{
			V: 1, Platform: "telegram", PlatformGroupID: "-100", ReplyTo: "7",
			DeliveryID: "d-react", Text: "reply", LifecycleFeedback: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return channel.OutboundOp{Kind: "send_group_reply", Payload: payload, Guard: guard}
	}

	t.Run("lost before send fires nothing", func(t *testing.T) {
		fake := &telegramAPIFake{responses: map[string][]string{}}
		b := newPublisherTestBot(t, fake)
		_, err := b.SendOperation(context.Background(), groupOp(func(context.Context) error {
			return fmt.Errorf("lease lost")
		}))
		if err == nil {
			t.Fatal("want error when ownership is lost")
		}
		var sendErr *channel.SendError
		if !errors.As(err, &sendErr) || sendErr.Class != channel.SendRetryable {
			t.Fatalf("err = %v, want retryable SendError", err)
		}
		if len(fake.calls) != 0 {
			t.Fatalf("platform calls = %d, want 0", len(fake.calls))
		}
	})

	t.Run("lost after send skips terminal reaction", func(t *testing.T) {
		fake := &telegramAPIFake{responses: map[string][]string{}}
		b := newPublisherTestBot(t, fake)
		var calls int
		_, err := b.SendOperation(context.Background(), groupOp(func(context.Context) error {
			calls++
			if calls >= 3 {
				return fmt.Errorf("lease lost")
			}
			return nil
		}))
		if err != nil {
			t.Fatalf("send succeeded before the lease moved: %v", err)
		}
		if got := len(fake.callsFor("sendMessage")); got != 1 {
			t.Fatalf("sendMessage calls = %d, want 1", got)
		}
		if got := len(fake.callsFor("setMessageReaction")); got != 1 {
			t.Fatalf("setMessageReaction calls = %d, want 1 (ack only, no terminal)", got)
		}
	})

	t.Run("owned op reacts and sends", func(t *testing.T) {
		fake := &telegramAPIFake{responses: map[string][]string{}}
		b := newPublisherTestBot(t, fake)
		if _, err := b.SendOperation(context.Background(), groupOp(func(context.Context) error { return nil })); err != nil {
			t.Fatalf("send: %v", err)
		}
		if got := len(fake.callsFor("sendMessage")); got != 1 {
			t.Fatalf("sendMessage calls = %d, want 1", got)
		}
		if got := len(fake.callsFor("setMessageReaction")); got != 2 {
			t.Fatalf("setMessageReaction calls = %d, want 2 (ack + terminal clear)", got)
		}
	})
}

// The artifact read can outlive the replica's lease: if ownership moves while
// the blob is being fetched, the shared read boundary re-checks the guard and
// the platform call never happens — the op stays pending for the new owner.
func TestAttachmentLeaseLossDuringReadSkipsSend(t *testing.T) {
	db := dbtest.New(t)
	createOutboxChannel(t, db, "ch-tg-art", "00000000-0000-0000-0000-0000000000aa")
	s := choutbox.New(db)
	fake := &telegramAPIFake{responses: map[string][]string{}}
	b := newPublisherTestBot(t, fake)
	b.bot.Me = &tele.User{ID: 1, Username: "bot-1"}
	b.handler = stubAttachmentOpener{
		db: db,
		onOpen: func() {
			// Another replica took the lease mid-read.
			if _, err := db.Exec(context.Background(),
				`UPDATE channel SET runtime_token = '00000000-0000-0000-0000-0000000000bb'::uuid WHERE id = 'ch-tg-art'`); err != nil {
				t.Errorf("steal lease: %v", err)
			}
		},
	}
	ctx := context.Background()

	ops, err := choutbox.ReplyChain("", "run:art", "ch-tg-art", "bot-1",
		tgAddress, "sess", []channel.Event{fileEvent()}, tgReplyPlan)
	if err != nil {
		t.Fatalf("ReplyChain: %v", err)
	}
	appendOps(t, db, s, ops)

	fake.responses["sendMessage"] = []string{`{"ok":true,"result":{"message_id":30}}`}
	// Reply lands, then the file op claims with the lease intact, loses it
	// inside the read, and must not issue sendDocument.
	for i := range 3 {
		if _, err := s.ProcessDue(ctx, "ch-tg-art", "00000000-0000-0000-0000-0000000000aa", b); err != nil {
			t.Fatalf("ProcessDue %d: %v", i, err)
		}
	}
	if calls := len(fake.callsFor("sendDocument")); calls != 0 {
		t.Fatalf("sendDocument calls = %d, want 0 (fenced-out replica)", calls)
	}
	if got := opState(t, db, "run:art", 1); got != "pending" {
		t.Fatalf("file op = %s, want pending for the new owner", got)
	}
}
