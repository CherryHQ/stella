package discord

import (
	"context"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
)

func (b *Bot) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) error {
	targetID := req.PlatformGroupID
	if req.PlatformThreadID != "" {
		targetID = req.PlatformThreadID
	}
	stopTyping := b.startTypingHeartbeat(targetID)
	defer stopTyping()
	var cancel *cancelControl
	if req.Abort != nil {
		cancel = &cancelControl{requesterID: req.RequesterID, abort: req.Abort}
	}
	err := b.deliverStream(ctx, targetID, req.ReplyTo, req.Stream, cancel)
	// The triggering message's 👀 crosses into this async dispatch by ReplyTo:
	// handleMessage only marks a group turn pending, it never finalizes it.
	b.finishReaction(context.WithoutCancel(ctx), targetID, req.ReplyTo, err == nil)
	return err
}
