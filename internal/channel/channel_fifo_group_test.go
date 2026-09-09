package channel

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestEnqueueGroupResponderTxDeduplicatesRouteAgent(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	ctx := context.Background()
	in := GroupResponderEnqueue{
		RouteID: fx.message.ID, MessageID: fx.message.ID, GroupID: fx.groupID,
		AgentID: "agent-1", ChannelID: "ch-1", Platform: "telegram",
		ChatKey: "physical-group-1", Seq: 1, Kind: groupRouteActionWake,
		Decision: "wake", Reason: "human message",
	}

	tx, err := fx.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	item, inserted, err := EnqueueGroupResponderTx(ctx, sqlc.New(tx), in)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("enqueue: %v", err)
	}
	if !inserted || item.Seq != 1 {
		_ = tx.Rollback(ctx)
		t.Fatalf("enqueue = (%s, %v), want seq 1 and inserted", item.ID, inserted)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	tx, err = fx.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	duplicate, inserted, err := EnqueueGroupResponderTx(ctx, sqlc.New(tx), in)
	if err != nil {
		t.Fatalf("duplicate enqueue: %v", err)
	}
	if inserted || duplicate.ID != item.ID {
		t.Fatalf("duplicate enqueue = (%s, %v), want (%s, false)", duplicate.ID, inserted, item.ID)
	}
	if _, err := sqlc.New(tx).GetChannelFIFOItemBySource(ctx, sqlc.GetChannelFIFOItemBySourceParams{
		BindingID: item.BindingID, SourceKey: "group-route:" + in.RouteID + ":" + in.AgentID,
	}); err != nil {
		t.Fatalf("check source key: %v", err)
	}
}

// The route transaction admits a responder as a FIFO item. The consumer then
// creates the mirrored accept/publish ledger under that item's identity and
// completes the same item only after the canonical reply reaches delivered.
// This is the end-to-end DB seam that prevents a route row from becoming a
// second model execution queue.
func TestGroupResponderFIFOConsumerCompletesAcceptedPublish(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := context.Background()
	tx, err := fx.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	item, inserted, err := EnqueueGroupResponderTx(ctx, sqlc.New(tx), GroupResponderEnqueue{
		RouteID: fx.message.ID, MessageID: fx.message.ID, GroupID: fx.groupID,
		AgentID: "agent-1", ChannelID: "ch-1", Platform: "web",
		ChatKey: "physical-group-1", Seq: fx.message.Seq, Kind: groupRouteActionWake,
		Decision: groupRouteActionWake, Reason: "open_floor",
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("admit group responder: %v", err)
	}
	if !inserted {
		_ = tx.Rollback(ctx)
		t.Fatal("first group responder admission unexpectedly deduplicated")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	owner := "group-fifo-test"
	claimed, err := fx.q.ClaimNextChannelFIFOItem(ctx, sqlc.ClaimNextChannelFIFOItemParams{
		LeaseOwner: owner, LeaseSeconds: 60,
	})
	if err != nil {
		t.Fatalf("claim group responder FIFO item: %v", err)
	}
	if claimed.ID != item.ID {
		t.Fatalf("claimed item = %s, want %s", claimed.ID, item.ID)
	}
	if err := fx.d.HandleFIFO(ctx, claimed); err != nil {
		t.Fatalf("handle group responder FIFO item: %v", err)
	}
	ledger, err := fx.q.GetGroupDispatch(ctx, item.ID)
	if err != nil {
		t.Fatalf("read group responder ledger: %v", err)
	}
	if ledger.Status != "completed" {
		t.Fatalf("ledger status after group execution = %q, want completed", ledger.Status)
	}
	if ledger.ResultMessageID == "" || !ledger.PublishedAt.Valid {
		t.Fatalf("ledger publish facts = result:%q published:%v, want both", ledger.ResultMessageID, ledger.PublishedAt.Valid)
	}
	result, err := fx.q.GetGroupMessage(ctx, ledger.ResultMessageID)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeliveryState != "delivered" {
		t.Fatalf("result delivery state = %q, want delivered", result.DeliveryState)
	}

	ingress := &DurableIngress{db: fx.db, owner: owner}
	if err := ingress.complete(ctx, claimed, "", false); err != nil {
		t.Fatalf("complete group responder FIFO item: %v", err)
	}
	completed, err := fx.q.GetChannelFIFOItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "completed" || !completed.ReleasedAt.Valid {
		t.Fatalf("FIFO state = %q released=%v, want completed/released", completed.State, completed.ReleasedAt.Valid)
	}
}

// The AgentRun completion is released only after the FIFO source is complete.
// If quota/item terminalization fails after the group publisher captured a
// delivered outcome, the source must be downgraded to unknown rather than
// reporting delivered while the FIFO claim is still live.
func TestGroupResponderFIFOCompletionFailureDoesNotAckDelivered(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	ctx := context.Background()
	fx.d.coord.SetGroupDispatcher(fx.d)
	publisher := &recordingGroupPublisher{}
	fx.d.publish.publishers.Register("ch-1", publisher)
	probe := newGroupPublishCompletionProbe()
	fx.d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{Complete: true})
		}
		events := make(chan pkgchannel.Event, 1)
		events <- pkgchannel.Event{Text: "reply"}
		close(events)
		return &pkgchannel.ChatStream{Events: events, SessionID: "group-fifo-test", Completion: probe}, nil
	}

	tx, err := fx.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	item, inserted, err := EnqueueGroupResponderTx(ctx, sqlc.New(tx), GroupResponderEnqueue{
		RouteID: fx.message.ID, MessageID: fx.message.ID, GroupID: fx.groupID,
		AgentID: "agent-1", ChannelID: "ch-1", Platform: "telegram",
		ChatKey: "physical-group-1", Seq: fx.message.Seq, Kind: groupRouteActionWake,
		Decision: groupRouteActionWake, Reason: "open_floor",
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("admit group responder: %v", err)
	}
	if !inserted {
		_ = tx.Rollback(ctx)
		t.Fatal("first group responder admission unexpectedly deduplicated")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := fx.db.Exec(ctx, `CREATE FUNCTION fail_group_fifo_complete_fn() RETURNS trigger AS $$ BEGIN IF NEW.state = 'completed' THEN RAISE EXCEPTION 'fail group FIFO completion'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql;`); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `CREATE TRIGGER fail_group_fifo_complete BEFORE UPDATE ON channel_fifo_item FOR EACH ROW EXECUTE FUNCTION fail_group_fifo_complete_fn();`); err != nil {
		t.Fatal(err)
	}

	ingress := &DurableIngress{db: fx.db, coord: fx.d.coord, owner: "group-fifo-completion-test", lease: time.Second}
	if err := ingress.processOne(ctx); err == nil {
		t.Fatal("FIFO completion failure must surface")
	}
	select {
	case <-probe.Done():
	case <-time.After(time.Second):
		t.Fatal("group AgentRun completion was not released")
	}
	if probe.outcome != pkgchannel.EgressUnknown {
		t.Fatalf("source completion outcome = %q, want unknown", probe.outcome)
	}
	item, err = fx.q.GetChannelFIFOItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.State == "completed" || item.ReleasedAt.Valid {
		t.Fatalf("FIFO item state = %q released=%v, want incomplete after injected failure", item.State, item.ReleasedAt.Valid)
	}
	if publisher.calls != 1 {
		t.Fatalf("publisher calls = %d, want one attempt", publisher.calls)
	}
}
