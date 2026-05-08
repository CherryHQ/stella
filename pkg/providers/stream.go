package providers

import (
	"sync"

	"github.com/CherryHQ/stella/pkg/ai"
)

// AssistantEventStream provides ordered assistant events.
type AssistantEventStream interface {
	Events() <-chan ai.AssistantEvent
	Close() error
	Wait() error
}

// ChannelEventStream is a channel-backed AssistantEventStream.
//
// Producers call Emit to send events, then Finish to close the stream and
// record any terminal error. Consumers range over Events() and then call
// Wait() to retrieve the terminal error.
//
// Close vs Finish: Finish is the normal shutdown path — it stores the error
// and closes the channel. Close is for consumer-side cancellation — it closes
// the channel without storing an error. If Close races with Finish, the first
// caller wins (via sync.Once) and Wait() returns whatever error was stored
// before the channel closed.
type ChannelEventStream struct {
	events chan ai.AssistantEvent
	once   sync.Once
	errMu  sync.RWMutex
	err    error
}

// NewChannelEventStream returns a writable channel stream.
func NewChannelEventStream(buffer int) *ChannelEventStream {
	return &ChannelEventStream{events: make(chan ai.AssistantEvent, buffer)}
}

// Events returns read-only event channel.
func (s *ChannelEventStream) Events() <-chan ai.AssistantEvent {
	return s.events
}

// Emit sends one event to subscribers.
func (s *ChannelEventStream) Emit(event ai.AssistantEvent) {
	s.events <- event
}

// Finish closes the event stream and records the terminal error.
// After Finish, Wait() returns err and Events() is drained.
// Safe to call concurrently with Close — first caller wins the channel close.
func (s *ChannelEventStream) Finish(err error) {
	s.errMu.Lock()
	s.err = err
	s.errMu.Unlock()
	s.once.Do(func() {
		close(s.events)
	})
}

// Close closes the event stream from the consumer side without recording an error.
// After Close, any pending Emit calls will panic (closed channel).
// If Finish was called first, Close is a no-op. If Close is called first,
// Finish still records the error for Wait() but does not re-close the channel.
func (s *ChannelEventStream) Close() error {
	s.once.Do(func() {
		close(s.events)
	})
	return nil
}

// Wait returns terminal stream error.
func (s *ChannelEventStream) Wait() error {
	s.errMu.RLock()
	defer s.errMu.RUnlock()
	return s.err
}
