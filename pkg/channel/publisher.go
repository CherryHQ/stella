package channel

import (
	"context"
	"fmt"
)

// GroupPublisher renders one accepted, complete group result to one concrete
// egress. It must not resolve sessions, call agents, or write event-log rows;
// the dispatcher owns those cross-platform concerns.
type GroupPublisher interface {
	Publish(ctx context.Context, req GroupPublishRequest) error
}

// GroupPublishRequest carries the routing and lifecycle facts needed by a
// platform publisher. DeliveryID is stable across retries of one accepted
// platform delivery, so adapters may use it for native idempotency.
type GroupPublishRequest struct {
	Platform         string
	PlatformGroupID  string
	PlatformThreadID string
	ReplyTo          string
	Stream           *ChatStream
	// DeliveryID is stable across retries of one accepted platform delivery.
	// Publishers use it for platform-native idempotency without learning about
	// dispatcher rows or persistence details.
	DeliveryID string

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
}

// ValidateGroupReplay consumes a replay before a publisher performs a
// platform side effect. A malformed or cancelled stream is returned as an
// error instead of becoming a visible platform failure message.
func ValidateGroupReplay(ctx context.Context, stream *ChatStream) (*ChatStream, error) {
	if stream == nil {
		return nil, nil
	}
	events := make([]Event, 0)
	var replayErr error
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case event, ok := <-stream.Events:
			if !ok {
				if replayErr != nil {
					return nil, replayErr
				}
				replay := make(chan Event, len(events))
				for _, event := range events {
					replay <- event
				}
				close(replay)
				return &ChatStream{Events: replay, SessionID: stream.SessionID}, nil
			}
			if event.Err != nil {
				if replayErr == nil {
					replayErr = fmt.Errorf("group replay stream: %w", event.Err)
				}
				continue
			}
			if replayErr == nil {
				events = append(events, event)
			}
		}
	}
}
