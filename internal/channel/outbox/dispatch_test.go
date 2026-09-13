package outbox

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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
// accountFenceSender reports that the configured account no longer owns the
// account key the op was enqueued under — the channel's platform account was
// swapped after enqueue.
type accountFenceSender struct {
	fakeSender
	owns bool
}

func (f *accountFenceSender) OwnsAccount(string) bool { return f.owns }

func TestDispatchAccountMismatchFailsWithoutSend(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	appendOps(t, s, db, []Op{op("d-1", 0)})

	// The channel now runs under a different platform account: the queued op
	// must fail account_mismatch without reaching the SDK.
	sender := &accountFenceSender{owns: false}
	n, err := s.ProcessDue(ctx, "ch-1", "", sender)
	if err != nil || n != 1 {
		t.Fatalf("ProcessDue: n=%d err=%v", n, err)
	}
	if len(sender.calls) != 0 {
		t.Fatalf("stale-account op reached the adapter: %d calls", len(sender.calls))
	}
	rows, _ := s.ListByDelivery(ctx, "d-1")
	if rows[0].State != StateFailed || !rows[0].ErrorCode.Valid || rows[0].ErrorCode.String != ErrCodeAccountMismatch {
		t.Fatalf("op = %s code=%v, want failed/account_mismatch", rows[0].State, rows[0].ErrorCode)
	}

	// Rotating credentials on the same account keeps the key: the same fence
	// passes and the op sends.
	sender2 := &accountFenceSender{owns: true}
	sender2.results = []sendResult{{receipt: pkgchannel.SendResult{PlatformMessageID: "m-1"}}}
	appendOps(t, s, db, []Op{op("d-2", 0)})
	if n, err := s.ProcessDue(ctx, "ch-1", "", sender2); err != nil || n != 1 {
		t.Fatalf("same-account ProcessDue: n=%d err=%v", n, err)
	}
	if len(sender2.calls) != 1 {
		t.Fatal("same-account op did not reach the adapter")
	}
}

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

// draftOp builds a pending draft_update op in the run's live delivery.
func draftOp(runID string, seq int64, index int) Op {
	payload, _ := json.Marshal(pkgchannel.DraftUpdatePayload{V: PayloadVersion, Seq: seq, Text: "progress"})
	addr, _ := json.Marshal(Address{V: AddressVersion, ChatKey: "chat-a"})
	return Op{
		RunID: runID, DeliveryKey: LiveDeliveryKey(runID), Index: index,
		Kind: OpDraftUpdate, ChannelID: "ch-1", AccountKey: "bot-1",
		Address: addr, Payload: payload,
	}
}

func replyOp(runID string) Op {
	payload, _ := json.Marshal(pkgchannel.ReplyOpPayload{V: PayloadVersion, Events: []pkgchannel.Event{{Text: "done"}}})
	addr, _ := json.Marshal(Address{V: AddressVersion, ChatKey: "chat-a"})
	return Op{
		RunID: runID, DeliveryKey: DeliveryKeyForRun(runID), Index: 0,
		Kind: OpSendReply, ChannelID: "ch-1", AccountKey: "bot-1",
		Address: addr, Payload: payload,
	}
}

func createRunRow(t *testing.T, db *pgxpool.Pool, state string) string {
	t.Helper()
	ctx := t.Context()
	q := sqlc.New(db)
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "agent-1", Name: "agent-1", Workspace: t.TempDir(),
		Sandbox: json.RawMessage("{}"), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID: uuid.Must(uuid.NewV7()).String(), SessionID: "sess-1", Kind: "chat",
		LastActive: time.Now().UTC(), AgentID: pgtype.Text{String: "agent-1", Valid: true},
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	run, err := q.CreateAgentRun(ctx, sqlc.CreateAgentRunParams{
		SessionID: "sess-1", AgentID: "agent-1", RequestKey: "req-1",
		Actor: json.RawMessage("{}"), Input: json.RawMessage("{}"),
		ReplyAddress: json.RawMessage("{}"), EnqueueSeq: 1,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := db.Exec(ctx, "UPDATE agent_run SET state=$2, finished_at=clock_timestamp() WHERE id=$1", run.ID, state); err != nil {
		t.Fatalf("set run state: %v", err)
	}
	return run.ID
}

// A draft successor must converge when its sending predecessor resolves to a
// non-sent state: failed here. The successor claims, sees the run is terminal
// (its progress can only be superseded now), cancels itself, and the terminal
// reply — held back by the same-run live-draft barrier — then dispatches.
func TestDraftSuccessorConvergesAfterFailedPredecessor(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()
	runID := createRunRow(t, db, "completed")

	appendOps(t, s, db, []Op{draftOp(runID, 1, 0)})
	d0, err := s.ListByDelivery(ctx, LiveDeliveryKey(runID))
	if err != nil || len(d0) != 1 {
		t.Fatalf("draft0 row: %v %d", err, len(d0))
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, ok, err := s.ClaimAttempt(ctx, tx, d0[0].ID, "")
	if err != nil || !ok {
		t.Fatalf("claim draft0: %v %v", ok, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Upsert while draft0 is in flight: draft1 appends with DependsOn=[0].
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if merged, err := s.UpsertDraft(ctx, tx, draftOp(runID, 2, 0), 2); err != nil || !merged {
		t.Fatalf("upsert draft1: %v %v", merged, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// draft0's attempt ends in a permanent platform rejection.
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.CompleteAttempt(ctx, tx, d0[0].ID, token, Outcome{State: StateFailed, ErrorCode: ErrCodePermanent}); err != nil || !ok {
		t.Fatalf("fail draft0: %v %v", ok, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	appendOps(t, s, db, []Op{replyOp(runID)})
	sender := &fakeSender{results: []sendResult{{receipt: pkgchannel.SendResult{PlatformMessageID: "final-1"}}}}

	// First sweep: draft1 is eligible (failed predecessor resolves the dep),
	// claims, and is canceled — the run already finished, so its progress can
	// never be needed. The terminal op still waits behind it this sweep.
	if n, err := s.ProcessDue(ctx, "ch-1", "", sender); err != nil || n != 1 {
		t.Fatalf("sweep 1: n=%d err=%v", n, err)
	}
	live, _ := s.ListByDelivery(ctx, LiveDeliveryKey(runID))
	if live[1].State != StateCanceled {
		t.Fatalf("draft1 = %s, want canceled", live[1].State)
	}

	// Second sweep: the barrier is clear and the terminal reply sends.
	if n, err := s.ProcessDue(ctx, "ch-1", "", sender); err != nil || n != 1 {
		t.Fatalf("sweep 2: n=%d err=%v", n, err)
	}
	rows, _ := s.ListByDelivery(ctx, DeliveryKeyForRun(runID))
	if rows[0].State != StateSent {
		t.Fatalf("terminal op = %s, want sent", rows[0].State)
	}
	if len(sender.calls) != 1 || sender.calls[0].Kind != OpSendReply {
		t.Fatalf("sender calls = %+v, want exactly the terminal reply", sender.calls)
	}
}

// Same convergence when the predecessor expires to 'unknown': the 5-minute
// attempt deadline far outlives any in-flight SDK call, so no zombie edit can
// still land after the successor — the dep resolves and the terminal reply
// is never permanently parked behind an unclaimable draft.
func TestDraftSuccessorConvergesAfterUnknownPredecessor(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()
	runID := createRunRow(t, db, "completed")

	appendOps(t, s, db, []Op{draftOp(runID, 1, 0)})
	d0, _ := s.ListByDelivery(ctx, LiveDeliveryKey(runID))
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, ok, err := s.ClaimAttempt(ctx, tx, d0[0].ID, "")
	if err != nil || !ok {
		t.Fatalf("claim draft0: %v %v", ok, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertDraft(ctx, tx, draftOp(runID, 2, 0), 2); err != nil {
		t.Fatalf("upsert draft1: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.CompleteAttempt(ctx, tx, d0[0].ID, token, Outcome{State: StateUnknown, ErrorCode: ErrCodeUnknown}); err != nil || !ok {
		t.Fatalf("unknown draft0: %v %v", ok, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	appendOps(t, s, db, []Op{replyOp(runID)})
	sender := &fakeSender{results: []sendResult{{receipt: pkgchannel.SendResult{PlatformMessageID: "final-1"}}}}
	for i := range 4 {
		if _, err := s.ProcessDue(ctx, "ch-1", "", sender); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
	}
	live, _ := s.ListByDelivery(ctx, LiveDeliveryKey(runID))
	if live[1].State != StateCanceled {
		t.Fatalf("draft1 = %s, want canceled", live[1].State)
	}
	rows, _ := s.ListByDelivery(ctx, DeliveryKeyForRun(runID))
	if rows[0].State != StateSent {
		t.Fatalf("terminal op = %s, want sent", rows[0].State)
	}
	if len(sender.calls) != 1 || sender.calls[0].Kind != OpSendReply {
		t.Fatalf("sender calls = %+v, want exactly the terminal reply", sender.calls)
	}
}

// gatedDraftSender models a platform whose apply can lag the SDK call: when
// gate is set, a draft edit returns SendUnknown to the dispatcher but the
// platform-side mutation lands only after the gate closes — the zombie edit
// of a SIGSTOP'd owner or a delayed platform apply.
type gatedDraftSender struct {
	fakeSender
	mu       sync.Mutex
	messages map[string]string
	nextID   int
	gate     chan struct{}
}

func (g *gatedDraftSender) create(text string) string {
	g.nextID++
	id := "msg-" + strconv.Itoa(g.nextID)
	g.messages[id] = text
	return id
}

func (g *gatedDraftSender) SendDraftUpdate(_ context.Context, op pkgchannel.OutboundOp) (pkgchannel.SendResult, error) {
	var payload pkgchannel.DraftUpdatePayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return pkgchannel.SendResult{}, err
	}
	g.mu.Lock()
	g.calls = append(g.calls, op)
	id := op.DraftMessageID
	gate := g.gate
	if id == "" {
		id = g.create(payload.Text)
		g.mu.Unlock()
		return pkgchannel.SendResult{PlatformMessageID: id}, nil
	}
	g.mu.Unlock()
	if gate != nil {
		// The edit reached the platform but its response/apply is stuck past
		// the attempt deadline: unverified, not dead.
		go func() {
			<-gate
			g.mu.Lock()
			g.messages[id] = payload.Text
			g.mu.Unlock()
		}()
		return pkgchannel.SendResult{}, pkgchannel.SendErrorf(pkgchannel.SendUnknown, "response lost after request sent")
	}
	g.mu.Lock()
	g.messages[id] = payload.Text
	g.mu.Unlock()
	return pkgchannel.SendResult{PlatformMessageID: id}, nil
}

func (g *gatedDraftSender) SendOperation(_ context.Context, op pkgchannel.OutboundOp) (pkgchannel.SendResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, op)
	var payload pkgchannel.ReplyOpPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return pkgchannel.SendResult{}, err
	}
	text := payload.Text
	if text == "" {
		text, _, _ = pkgchannel.CollectReplyEvents(payload.Events)
	}
	if op.DraftMessageID != "" {
		g.messages[op.DraftMessageID] = text
		return pkgchannel.SendResult{PlatformMessageID: op.DraftMessageID}, nil
	}
	return pkgchannel.SendResult{PlatformMessageID: g.create(text)}, nil
}

// A draft edit whose outcome is unknown may still be in flight — nothing can
// retract a request that already passed the ownership check. When such an op
// sits behind the run's draft identity, the terminal reply must abandon that
// message and land on a fresh one, so the zombie's late arrival can only
// stain the abandoned preview, never revert the final answer.
func TestTerminalReplyAbandonsPollutedDraftMessage(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()
	runID := createRunRow(t, db, "running")

	sender := &gatedDraftSender{messages: map[string]string{}}

	// draft0 sends cleanly and creates the preview message.
	appendOps(t, s, db, []Op{draftOp(runID, 1, 0)})
	if n, err := s.ProcessDue(ctx, "ch-1", "", sender); err != nil || n != 1 {
		t.Fatalf("draft0 sweep: n=%d err=%v", n, err)
	}
	live, _ := s.ListByDelivery(ctx, LiveDeliveryKey(runID))
	previewID := live[0].PlatformMessageID.String
	if previewID == "" {
		t.Fatal("draft0 recorded no platform message id")
	}

	// draft1 claims the same identity and its edit goes unknown mid-flight:
	// the platform apply is parked behind the gate.
	gate := make(chan struct{})
	sender.gate = gate
	d1 := draftOp(runID, 2, 1)
	d1.DependsOn = []int{0}
	appendOps(t, s, db, []Op{d1})
	if n, err := s.ProcessDue(ctx, "ch-1", "", sender); err != nil || n != 1 {
		t.Fatalf("draft1 sweep: n=%d err=%v", n, err)
	}
	live, _ = s.ListByDelivery(ctx, LiveDeliveryKey(runID))
	if live[1].State != StateUnknown {
		t.Fatalf("draft1 = %s, want unknown", live[1].State)
	}

	// The run completes; the terminal reply must NOT reuse the polluted
	// message id — a zombie edit can still land on it.
	if _, err := db.Exec(ctx, "UPDATE agent_run SET state='completed', finished_at=clock_timestamp() WHERE id=$1", runID); err != nil {
		t.Fatal(err)
	}
	appendOps(t, s, db, []Op{replyOp(runID)})
	if n, err := s.ProcessDue(ctx, "ch-1", "", sender); err != nil || n != 1 {
		t.Fatalf("terminal sweep: n=%d err=%v", n, err)
	}
	replyCall := sender.calls[len(sender.calls)-1]
	if replyCall.Kind != OpSendReply {
		t.Fatalf("last call kind = %s", replyCall.Kind)
	}
	if replyCall.DraftMessageID != "" {
		t.Fatalf("terminal reused polluted draft id %q", replyCall.DraftMessageID)
	}
	rows, _ := s.ListByDelivery(ctx, DeliveryKeyForRun(runID))
	finalID := rows[0].PlatformMessageID.String
	if rows[0].State != StateSent || finalID == "" || finalID == previewID {
		t.Fatalf("terminal = %s msg %q, want sent on a fresh message", rows[0].State, finalID)
	}

	// The zombie edit now lands — on the abandoned preview only.
	close(gate)
	deadline := time.Now().Add(5 * time.Second)
	for {
		sender.mu.Lock()
		stale := sender.messages[previewID]
		final := sender.messages[finalID]
		sender.mu.Unlock()
		if stale == "progress" && final == "done" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("zombie edit: preview=%q final=%q, want stale preview + intact final", stale, final)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A non-draft op chained behind a permanently failed predecessor must not
// park forever: the delivery is already broken, so the claim sweep cancels it.
func TestBlockedSuccessorCanceledAfterFailedDependency(t *testing.T) {
	db := dbtest.New(t)
	createChannel(t, db, "ch-1")
	s := New(db)
	ctx := t.Context()

	o0, o1 := op("d-1", 0), op("d-1", 1)
	o1.DependsOn = []int{0}
	appendOps(t, s, db, []Op{o0, o1})

	sender := &fakeSender{results: []sendResult{
		{err: pkgchannel.SendErrorf(pkgchannel.SendPermanent, "chat deleted")},
	}}
	if n, err := s.ProcessDue(ctx, "ch-1", "", sender); err != nil || n != 1 {
		t.Fatalf("sweep 1: n=%d err=%v", n, err)
	}
	// The failed head cancels the queued tail in the next claim pass.
	if _, err := s.ProcessDue(ctx, "ch-1", "", sender); err != nil {
		t.Fatalf("sweep 2: %v", err)
	}
	states := outboxStates(t, db)
	if states[0] != StateFailed || states[1] != StateCanceled {
		t.Fatalf("states = %v, want failed/canceled", states)
	}
}
