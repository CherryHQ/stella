package channel

import "context"

// DurablePublishRequest contains the immutable routing coordinates needed to
// finish an accepted input after its original listener has disappeared.
type DurablePublishRequest struct {
	DeliveryID        string
	Platform          string
	ChannelID         string
	ChatID            string
	ThreadID          string
	TargetID          string
	ReplyTo           string
	MessageID         string
	IsGroup           bool
	LifecycleFeedback bool
	Stream            *ChatStream
}

// DurablePublisher renders an accepted input without an ingress listener.
// Every outbound operation checks the Stream fence, and final Ack records the
// delivered, discarded-before-send, or unknown outcome after all sends settle.
type DurablePublisher interface {
	PublishIncoming(context.Context, DurablePublishRequest) error
}
