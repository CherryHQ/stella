package channel

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type privatePublishReconstructor struct {
	publisher pkgchannel.DurablePublisher
}

func (r privatePublishReconstructor) ReconstructGroupPublisher(context.Context, config.Channel, GroupOutboxEnvelope) (pkgchannel.GroupPublisher, error) {
	return nil, nil
}

func (r privatePublishReconstructor) ReconstructIncomingPublisher(context.Context, config.Channel, GroupOutboxEnvelope) (pkgchannel.DurablePublisher, error) {
	return r.publisher, nil
}

type privatePublishAckPublisher struct{}

func (privatePublishAckPublisher) PublishIncoming(ctx context.Context, req pkgchannel.DurablePublishRequest) error {
	for range req.Stream.Events {
	}
	return req.Stream.Completion.Ack(ctx, pkgchannel.EgressDelivered)
}

type privatePublishCompletion struct {
	mu      sync.Mutex
	outcome pkgchannel.EgressOutcome
	done    chan struct{}
}

func newPrivatePublishCompletion() *privatePublishCompletion {
	return &privatePublishCompletion{done: make(chan struct{})}
}

func (*privatePublishCompletion) Check(context.Context) error { return nil }

func (c *privatePublishCompletion) Ack(_ context.Context, outcome pkgchannel.EgressOutcome) error {
	c.mu.Lock()
	if c.outcome == "" {
		c.outcome = outcome
		close(c.done)
	}
	c.mu.Unlock()
	return nil
}

func (c *privatePublishCompletion) Done() <-chan struct{} { return c.done }

func (c *privatePublishCompletion) Outcome() pkgchannel.EgressOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outcome
}

// The private/restart publisher can receive Delivered before the FIFO item is
// terminalized. If complete fails, forwarding Delivered would release the
// adapter's queue despite an unknown durable settlement. The production
// publishWithoutWaiter path must downgrade the source acknowledgement first.
func TestDurableIngressPrivatePublishDowngradesAckWhenSettlementFails(t *testing.T) {
	c, db, _ := newDurableAcceptanceCoordinator(t)
	c.publisherReconstructor = privatePublishReconstructor{publisher: privatePublishAckPublisher{}}
	d := NewDurableIngress(db, "private-publish-settlement")
	d.BindCoordinator(c)

	source := newPrivatePublishCompletion()
	events := make(chan pkgchannel.Event)
	close(events)
	stream := &pkgchannel.ChatStream{Events: events, Completion: source}
	item := sqlc.ChannelFifoItem{ID: uuid.NewString(), PrincipalKey: "missing-principal"}
	envelope := durableIngressPayload{Message: pkgchannel.IncomingMessage{
		Platform: "telegram", ChannelID: "telegram", ChatID: "private-chat", SenderID: "target",
	}}

	err := d.publishWithoutWaiter(context.Background(), item, envelope, stream)
	if err == nil {
		t.Fatal("publishWithoutWaiter succeeded despite a failed FIFO settlement")
	}
	select {
	case <-source.Done():
	case <-time.After(time.Second):
		t.Fatal("source completion was not forwarded")
	}
	if got := source.Outcome(); got != pkgchannel.EgressUnknown {
		t.Fatalf("source completion outcome = %q, want %q after settlement failure", got, pkgchannel.EgressUnknown)
	}
}
