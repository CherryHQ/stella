package channel

import (
	"context"
	"sync"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

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
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
