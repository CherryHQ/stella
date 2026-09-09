package channel

import (
	"context"
	"sync"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// DurablePublisherReconstructor is the channel-runtime boundary used by a
// dispatcher to build an egress client on demand. The channel row supplies
// durable (and, where applicable, encrypted) credentials; the outbox envelope
// supplies immutable reply metadata captured at ingress. Implementations must
// not depend on a managed listener or a process-local PublisherRegistry.
//
// The interface lives in internal/channel so only the composition root can
// bind concrete plugin constructors. This keeps platform credentials and
// plugin imports out of the durable dispatcher.
type DurablePublisherReconstructor interface {
	ReconstructGroupPublisher(context.Context, config.Channel, GroupOutboxEnvelope) (pkgchannel.GroupPublisher, error)
	ReconstructIncomingPublisher(context.Context, config.Channel, GroupOutboxEnvelope) (pkgchannel.DurablePublisher, error)
}

// PublisherRegistry is the internal routing table for channel egress.
type PublisherRegistry struct {
	mu         sync.RWMutex
	publishers map[string]pkgchannel.GroupPublisher
}

func NewPublisherRegistry() *PublisherRegistry {
	return &PublisherRegistry{publishers: make(map[string]pkgchannel.GroupPublisher)}
}

func (r *PublisherRegistry) Register(channelID string, publisher pkgchannel.GroupPublisher) {
	if r == nil || channelID == "" || publisher == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishers[channelID] = publisher
}

func (r *PublisherRegistry) Unregister(channelID string) {
	if r == nil || channelID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.publishers, channelID)
}

func (r *PublisherRegistry) Get(channelID string) (pkgchannel.GroupPublisher, bool) {
	if r == nil || channelID == "" {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	publisher, ok := r.publishers[channelID]
	return publisher, ok
}

type noopGroupPublisher struct{}

func NoopGroupPublisher() pkgchannel.GroupPublisher { return noopGroupPublisher{} }

func (noopGroupPublisher) Publish(ctx context.Context, req pkgchannel.GroupPublishRequest) error {
	if req.Stream == nil {
		return nil
	}
	for {
		select {
		case _, ok := <-req.Stream.Events:
			if !ok {
				// Web/group canonical event persistence happens before this no-op
				// publisher is called. Acknowledge only after the replay has been
				// consumed so the owning queue cannot release at model EOF.
				return req.Stream.Ack(ctx, pkgchannel.EgressDelivered)
			}
		case <-ctx.Done():
			// Cancellation says nothing about whether the canonical event-log
			// consumer observed the replay. Keep the result unknown so a caller
			// cannot mistake a lost wait for a proven absent egress.
			_ = req.Stream.Ack(context.WithoutCancel(ctx), pkgchannel.EgressUnknown)
			return ctx.Err()
		}
	}
}

// PublishIncoming satisfies the durable incoming-publisher contract for Web.
// Web group replies are already appended to the canonical event log before
// this publisher runs, so consuming the replay and acknowledging it is the
// complete delivery operation. There is no platform request to make.
func (p noopGroupPublisher) PublishIncoming(ctx context.Context, req pkgchannel.DurablePublishRequest) error {
	return p.Publish(ctx, pkgchannel.GroupPublishRequest{Stream: req.Stream})
}
