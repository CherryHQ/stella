package discord

import (
	"cmp"
	"context"

	"github.com/CherryHQ/stella/pkg/channel"
)

// NewDurableGroupPublisher constructs REST egress without opening a Gateway.
func NewDurableGroupPublisher(cfg Config) (channel.GroupPublisher, error) {
	return New(cfg, nil)
}

func (b *Bot) PublishIncoming(ctx context.Context, req channel.DurablePublishRequest) error {
	return b.Publish(ctx, channel.GroupPublishRequest{
		Platform: req.Platform, PlatformGroupID: cmp.Or(req.ChatID, req.TargetID),
		PlatformThreadID: req.ThreadID, ReplyTo: cmp.Or(req.MessageID, req.ReplyTo),
		DeliveryID: req.DeliveryID, RequesterID: req.TargetID,
		LifecycleFeedback: req.LifecycleFeedback, Stream: req.Stream,
	})
}
