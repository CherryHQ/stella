package channel

import (
	"context"
	"sync"

	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// GroupPublisher renders and sends an agent response stream to one concrete
// group egress. It must not resolve sessions, call agents, or write event-log
// rows; the dispatcher owns those cross-platform concerns. Returning nil means
// the platform API confirmed delivery for platform publishers, or the stream was
// fully consumed for Web publishers (client write errors do not make Web publish
// fail; the event log is the durable delivery channel).
type GroupPublisher interface {
	Publish(ctx context.Context, req GroupPublishRequest) error
}

// DurablePublisherReconstructor is the channel-runtime boundary used by a Run
// executor to build an egress client on demand. The channel row supplies the
// durable (and, where applicable, encrypted) credentials; the outbox envelope
// supplies immutable reply/capability metadata captured at ingress. Implementations
// must not rely on a managed listener or a process-local PublisherRegistry.
type DurablePublisherReconstructor interface {
	ReconstructGroupPublisher(context.Context, config.Channel, GroupOutboxEnvelope) (GroupPublisher, error)
}

type GroupPublishRequest struct {
	GroupID          string
	AgentID          string
	AgentName        string
	ReplyChannelID   string
	Platform         string
	PlatformGroupID  string
	PlatformThreadID string
	ReplyTo          string
	Stream           *pkgchannel.ChatStream

	// RequesterID is the platform-native user ID of the human whose message
	// triggered this dispatch (eventlog's platform sender id, not a Stella
	// user ID). Publishers that offer a cancel affordance use it to reject a
	// click from anyone else. Empty when the trigger had no human actor.
	RequesterID string
	// LifecycleFeedback is true only when the triggering platform message
	// explicitly addressed this bot. Ambient semantic-routing turns must not
	// receive unsolicited completion reactions.
	LifecycleFeedback bool
	// Abort cancels the in-flight turn this dispatch is running, if any.
	// It is safe to call multiple times and from any goroutine; a publisher
	// invokes it at most once per accepted cancel click. Nil when the
	// dispatcher offers no cancellation for this request.
	Abort func() bool
	// FinalAttempt reports that the dispatcher will not requeue a returned
	// publish error. Publishers may use it for terminal platform feedback;
	// earlier attempts should return errors without claiming delivery ended.
	FinalAttempt bool
}

type PublisherRegistry struct {
	mu         sync.RWMutex
	publishers map[string]GroupPublisher
}

func NewPublisherRegistry() *PublisherRegistry {
	return &PublisherRegistry{publishers: make(map[string]GroupPublisher)}
}

func (r *PublisherRegistry) Register(channelID string, publisher GroupPublisher) {
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

func (r *PublisherRegistry) Get(channelID string) (GroupPublisher, bool) {
	if r == nil || channelID == "" {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	publisher, ok := r.publishers[channelID]
	return publisher, ok
}

type noopGroupPublisher struct{}

func NoopGroupPublisher() GroupPublisher { return noopGroupPublisher{} }

func (noopGroupPublisher) Publish(ctx context.Context, req GroupPublishRequest) error {
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
