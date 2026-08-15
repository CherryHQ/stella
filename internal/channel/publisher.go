package channel

import (
	"context"
	"sync"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// GroupPublisher renders and sends an agent response to one concrete group
// egress. It must not resolve sessions, call agents, or write event-log rows;
// the dispatcher owns those cross-platform concerns. Returning nil means the
// platform API confirmed delivery for platform publishers, or the stream was
// fully consumed for Web publishers (client write errors do not make Web publish
// fail; the event log is the durable delivery channel).
//
// A request arrives in one of two shapes and a publisher must handle both:
//
//   - live (Stream set): the agent is generating now, DeliveryCursor is 0.
//   - re-delivery (Text set): the agent already ran, its response is persisted,
//     and this call finishes a delivery a previous attempt left incomplete.
//     The agent must never run again for this dispatch.
type GroupPublisher interface {
	Publish(ctx context.Context, req GroupPublishRequest) error
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
	// Stream is a live agent turn. Exactly one of Stream and Text is set.
	Stream *pkgchannel.ChatStream
	// Text is the canonical persisted response for a re-delivery. Render it the
	// way a finished stream is rendered, minus progress affordances: there is no
	// turn in flight, so no draft, no tool progress, and no cancel button.
	// Images and files are not re-delivered — they are not persisted.
	Text string
	// DeliveryCursor is how many leading chunks of Text a previous attempt
	// confirmed the platform accepted. Always 0 for a live Stream. A publisher
	// that confirms chunks through Confirm must skip exactly that many chunks of
	// its own deterministic split; one that does not confirm must ignore this and
	// resend the whole response (at-least-once).
	DeliveryCursor int64
	// MarkDelivered durably records that the first n chunks reached the platform.
	// Call it through Confirm. Nil when the dispatcher offers no resume for this
	// request (synchronous Web publish, tests).
	MarkDelivered func(ctx context.Context, n int64) error
	// RecordResult durably persists the response this publisher is about to
	// send. Call it through Record. Nil for a re-delivery (already persisted)
	// and when the dispatcher offers no persistence for this request.
	RecordResult func(ctx context.Context, text string) error

	// RequesterID is the platform-native user ID of the human whose message
	// triggered this dispatch (eventlog's platform sender id, not a Stella
	// user ID). Publishers that offer a cancel affordance use it to reject a
	// click from anyone else. Empty when the trigger had no human actor.
	RequesterID string
	// Abort cancels the in-flight turn this dispatch is running, if any.
	// It is safe to call multiple times and from any goroutine; a publisher
	// invokes it at most once per accepted cancel click. Nil when the
	// dispatcher offers no cancellation for this request.
	Abort func() bool
}

// Confirm records that the first n chunks of this response reached the
// platform, so a later attempt resumes after them instead of resending them.
// Call it immediately after each chunk the platform accepted, with n = index+1.
//
// A publisher must treat a Confirm error as a delivery failure and stop: the
// error means this attempt no longer owns the dispatch, and continuing would
// deliver chunks that another attempt is also delivering.
func (r GroupPublishRequest) Confirm(ctx context.Context, n int64) error {
	if r.MarkDelivered == nil {
		return nil
	}
	return r.MarkDelivered(ctx, n)
}

// Record durably persists the response text before it is delivered, so a
// crash mid-delivery resumes instead of re-running the agent. A publisher that
// buffers the whole response before sending it — Discord splits into chunks, so
// it must — should call this once the stream has finished successfully and
// before the first message reaches the platform. Everything between "the agent
// finished" and "something durable proves it" is a window in which a crash
// costs a second agent turn; this call is how a publisher closes its own.
//
// Pass the exact text about to be sent: the persisted response is what a
// re-delivery splits, so the chunk a cursor points at only means the same thing
// if both attempts split the same string. Empty text records nothing.
//
// A publisher that returns after this call still leaves the response durable,
// so the dispatcher re-delivers it; a publisher that never calls it is not
// broken, only later — the dispatcher persists the response after Publish
// returns. Treat an error here as a delivery failure and send nothing: the
// response is unproven, and a retry would answer the group twice.
func (r GroupPublishRequest) Record(ctx context.Context, text string) error {
	if r.RecordResult == nil {
		return nil
	}
	return r.RecordResult(ctx, text)
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

// Publish drains the stream and delivers nothing. A re-delivery (Stream nil)
// is a no-op by construction: the response is already in the event log, which
// is the only delivery channel a Web group has.
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
