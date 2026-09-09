package channel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type privatePublishNoAckPublisher struct{}

// PublishIncoming returns without ever reporting an egress outcome: the
// scenario under test is a publisher that finishes silently, so nothing may
// block on the stream either.
func (privatePublishNoAckPublisher) PublishIncoming(context.Context, pkgchannel.DurablePublishRequest) error {
	return nil
}

// Regression: the fixed lease timer used to force-Ack EgressUnknown while the
// publisher was still streaming with a valid renewed claim. The source must
// stay live while its publisher work and authority are live; uncertainty comes
// only from real loss, cancellation, or egress failure.
func TestDurableIngressPrivatePublishStaysLiveUntilPublisherOutcome(t *testing.T) {
	c, db, _ := newDurableAcceptanceCoordinator(t)
	c.publisherReconstructor = privatePublishReconstructor{publisher: privatePublishAckPublisher{}}
	d := NewDurableIngress(db, "publish-liveness")
	d.BindCoordinator(c)
	// The old implementation borrowed d.lease as a total request deadline; a
	// 20ms value made any turn longer than 20ms fail prematurely.
	d.lease = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	message := pkgchannel.IncomingMessage{
		MessageID: "publish-liveness", Platform: "telegram", ChannelID: "telegram",
		SenderID: "sender-1", ChatID: "liveness-chat",
	}
	if _, _, _, err := d.Admit(ctx, message, "", ""); err != nil {
		t.Fatal(err)
	}
	item, err := sqlc.New(db).ClaimNextChannelFIFOItem(ctx, sqlc.ClaimNextChannelFIFOItemParams{
		LeaseOwner: d.owner, LeaseSeconds: 60,
	})
	if err != nil {
		t.Fatalf("claim admitted item: %v", err)
	}
	source := newPrivatePublishCompletion()
	events := make(chan pkgchannel.Event)
	stream := &pkgchannel.ChatStream{Events: events, Completion: source}
	// The publisher drains events; keep it joinable even if an assertion fails
	// before the test closes the stream itself.
	closeEvents := sync.OnceFunc(func() { close(events) })
	done := make(chan error, 1)
	var worker sync.WaitGroup
	worker.Go(func() { done <- d.publishWithoutWaiter(ctx, item, durableIngressPayload{Message: message}, stream) })
	defer func() {
		closeEvents()
		worker.Wait()
	}()

	select {
	case <-source.Done():
		t.Fatal("source settled before stream EOF without any egress failure")
	case <-time.After(200 * time.Millisecond):
	}
	var state, code string
	if err := db.QueryRow(ctx, "SELECT state, error_code FROM channel_fifo_item WHERE id = $1", item.ID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "running" || code != "" {
		t.Fatalf("live source was disturbed: state=%s code=%q", state, code)
	}

	closeEvents()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("publishWithoutWaiter: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("publishWithoutWaiter did not settle: %v", ctx.Err())
	}
	if got := source.Outcome(); got != pkgchannel.EgressDelivered {
		t.Fatalf("source outcome = %q, want delivered", got)
	}
	if err := db.QueryRow(ctx, "SELECT state FROM channel_fifo_item WHERE id = $1", item.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("FIFO state after delivered publish = %q, want completed", state)
	}
}

// A publisher that returns without acknowledging cannot prove the external
// outcome; the item must stay blocked for explicit recovery and the source
// must learn unknown, never delivered.
func TestDurableIngressPrivatePublishWithoutAckBlocksForRecovery(t *testing.T) {
	c, db, _ := newDurableAcceptanceCoordinator(t)
	c.publisherReconstructor = privatePublishReconstructor{publisher: privatePublishNoAckPublisher{}}
	d := NewDurableIngress(db, "publish-no-ack")
	d.BindCoordinator(c)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	message := pkgchannel.IncomingMessage{
		MessageID: "publish-no-ack", Platform: "telegram", ChannelID: "telegram",
		SenderID: "sender-1", ChatID: "no-ack-chat",
	}
	if _, _, _, err := d.Admit(ctx, message, "", ""); err != nil {
		t.Fatal(err)
	}
	item, err := sqlc.New(db).ClaimNextChannelFIFOItem(ctx, sqlc.ClaimNextChannelFIFOItemParams{
		LeaseOwner: d.owner, LeaseSeconds: 60,
	})
	if err != nil {
		t.Fatalf("claim admitted item: %v", err)
	}
	source := newPrivatePublishCompletion()
	events := make(chan pkgchannel.Event)
	close(events)
	stream := &pkgchannel.ChatStream{Events: events, Completion: source}
	// A silent publisher is handled as an unconfirmed outcome, not a returned
	// failure: the item is blocked for explicit recovery and the call returns.
	_ = d.publishWithoutWaiter(ctx, item, durableIngressPayload{Message: message}, stream)
	if got := source.Outcome(); got != pkgchannel.EgressUnknown {
		t.Fatalf("source outcome = %q, want unknown", got)
	}
	var state, code string
	if err := db.QueryRow(ctx, "SELECT state, error_code FROM channel_fifo_item WHERE id = $1", item.ID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" || code != "egress_unknown" {
		t.Fatalf("unconfirmed publish = %s/%q, want blocked/egress_unknown", state, code)
	}
}

// Regression: a pre-admission chat failure used to terminalize the mirrored
// ledger as failed, so the second FIFO attempt saw terminal work and completed
// the item without ever rerunning the chat. The ledger must stay re-claimable
// and the FIFO must actually retry the unlinked work.
func TestGroupFIFOPreAdmissionFailureIsRetriedNotCompleted(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	fx.d.coord.SetGroupDispatcher(fx.d)
	probe := newGroupPublishCompletionProbe()
	calls := 0
	fx.d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("temporary error before AgentRun admission")
		}
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{Complete: true})
		}
		events := make(chan pkgchannel.Event, 1)
		events <- pkgchannel.Event{Text: "reply"}
		close(events)
		return &pkgchannel.ChatStream{Events: events, SessionID: "group-retry-test", Completion: probe}, nil
	}
	item := enqueueGroupResponderForTest(t, fx)
	d := &DurableIngress{db: fx.db, owner: "group-retry-test", coord: fx.d.coord, lease: time.Minute}
	claim := func() sqlc.ChannelFifoItem {
		t.Helper()
		row, err := fx.q.ClaimNextChannelFIFOItem(ctx, sqlc.ClaimNextChannelFIFOItemParams{LeaseOwner: d.owner, LeaseSeconds: 60})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}

	first := claim()
	if err := d.processGroupFIFO(ctx, ctx, first); err == nil {
		t.Fatal("expected the pre-admission failure to surface")
	}
	pending, err := fx.q.GetChannelFIFOItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != "pending" || pending.RunID.Valid {
		t.Fatalf("FIFO after pre-admission failure = state %s run %v, want pending/unlinked", pending.State, pending.RunID.Valid)
	}
	ledger, err := fx.q.GetGroupDispatch(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.Status == "failed" || ledger.Status == "completed" || ledger.Status == "silent" || ledger.Status == "held" {
		t.Fatalf("ledger status after retryable failure = %q, want non-terminal", ledger.Status)
	}
	if ledger.LastError == "" {
		t.Fatal("ledger lost the failure detail")
	}

	if _, err := fx.db.Exec(ctx, `UPDATE channel_fifo_item SET next_attempt_at = now() WHERE id = $1`, item.ID); err != nil {
		t.Fatal(err)
	}
	second := claim()
	if err := d.processGroupFIFO(ctx, ctx, second); err != nil {
		t.Fatalf("second FIFO attempt: %v", err)
	}
	if calls != 2 {
		t.Fatalf("chat calls = %d, want the retry to actually rerun the chat", calls)
	}
	completed, err := fx.q.GetChannelFIFOItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "completed" || !completed.ReleasedAt.Valid {
		t.Fatalf("FIFO after retry = %s released %v, want completed", completed.State, completed.ReleasedAt.Valid)
	}
	final, err := fx.q.GetGroupDispatch(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != "completed" || final.ResultMessageID == "" || !final.PublishedAt.Valid {
		t.Fatalf("ledger after retry = %s result %q published %v, want completed with publish facts",
			final.Status, final.ResultMessageID, final.PublishedAt.Valid)
	}
	select {
	case <-probe.Done():
	case <-time.After(time.Second):
		t.Fatal("turn completion was not forwarded")
	}
	if probe.outcome != pkgchannel.EgressDelivered {
		t.Fatalf("turn outcome = %q, want delivered", probe.outcome)
	}
}

// The group FIFO consumer must also stay live while HandleFIFO runs longer
// than d.lease; no borrowed deadline may disturb a live group turn.
func TestGroupFIFOStaysLiveWhileHandleFIFORuns(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	fx.d.coord.SetGroupDispatcher(fx.d)
	probe := newGroupPublishCompletionProbe()
	// release unblocks the chat exactly once, whether the turn finishes or an
	// assertion fails first, and the turn goroutine is always joined.
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	fx.d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		<-release
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{Complete: true})
		}
		events := make(chan pkgchannel.Event, 1)
		events <- pkgchannel.Event{Text: "reply"}
		close(events)
		return &pkgchannel.ChatStream{Events: events, SessionID: "group-liveness-test", Completion: probe}, nil
	}
	item := enqueueGroupResponderForTest(t, fx)
	// A 20ms lease used to be borrowed as a request deadline mid-turn.
	d := &DurableIngress{db: fx.db, owner: "group-liveness", coord: fx.d.coord, lease: 20 * time.Millisecond}
	claimed, err := fx.q.ClaimNextChannelFIFOItem(ctx, sqlc.ClaimNextChannelFIFOItemParams{LeaseOwner: d.owner, LeaseSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != item.ID {
		t.Fatalf("claimed item = %s, want %s", claimed.ID, item.ID)
	}
	done := make(chan error, 1)
	var worker sync.WaitGroup
	worker.Go(func() { done <- d.processGroupFIFO(ctx, ctx, claimed) })
	defer func() {
		unblock()
		worker.Wait()
	}()

	select {
	case err := <-done:
		t.Fatalf("processGroupFIFO returned while the turn was still live: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	var state, code string
	if err := fx.db.QueryRow(ctx, "SELECT state, error_code FROM channel_fifo_item WHERE id = $1", item.ID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "running" || code != "" {
		t.Fatalf("live group turn was disturbed: state=%s code=%q", state, code)
	}

	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("processGroupFIFO: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("processGroupFIFO did not settle after the turn finished")
	}
	if probe.outcome != pkgchannel.EgressDelivered {
		t.Fatalf("turn outcome = %q, want delivered", probe.outcome)
	}
	completed, err := fx.q.GetChannelFIFOItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "completed" {
		t.Fatalf("FIFO state = %q, want completed", completed.State)
	}
}

func enqueueGroupResponderForTest(t *testing.T, fx dispatcherFixture) sqlc.ChannelFifoItem {
	t.Helper()
	ctx := context.Background()
	tx, err := fx.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	item, inserted, err := EnqueueGroupResponderTx(ctx, sqlc.New(tx), GroupResponderEnqueue{
		RouteID: fx.message.ID, MessageID: fx.message.ID, GroupID: fx.groupID,
		AgentID: "agent-1", ChannelID: "ch-1", Platform: "web",
		ChatKey: "physical-group-1", Seq: fx.message.Seq, Kind: groupRouteActionWake,
		Decision: groupRouteActionWake, Reason: "open_floor",
	})
	if err != nil {
		t.Fatalf("admit group responder: %v", err)
	}
	if !inserted {
		t.Fatal("group responder admission unexpectedly deduplicated")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return item
}
