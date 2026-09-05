package discord

import (
	"context"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

func (b *Bot) Publish(ctx context.Context, req pkgchannel.GroupPublishRequest) error {
	stream, err := pkgchannel.ValidateGroupReplay(ctx, req.Stream)
	if err != nil {
		return err
	}
	req.Stream = stream
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
	err = b.deliverGroupReplay(ctx, targetID, req.ReplyTo, req.Stream, cancel)
	// Only explicitly addressed turns opt into reaction lifecycle feedback.
	// Ambient semantic-routing messages must not receive an unsolicited naked
	// terminal reaction when no 👀 acknowledgement was started.
	if req.LifecycleFeedback {
		if err == nil {
			b.finishReaction(context.WithoutCancel(ctx), targetID, req.ReplyTo, true)
		} else {
			b.clearReactionLifecycle(context.WithoutCancel(ctx), targetID, req.ReplyTo)
		}
	}
	return err
}
