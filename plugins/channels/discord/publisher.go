package discord

import (
	"context"
	"strings"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
)

func (b *Bot) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) error {
	targetID := req.PlatformGroupID
	if req.PlatformThreadID != "" {
		targetID = req.PlatformThreadID
	}
	stopTyping := b.startTypingHeartbeat(targetID)
	defer stopTyping()
	resume := textDelivery{cursor: req.DeliveryCursor, confirm: req.Confirm}
	var err error
	if req.Stream == nil {
		err = b.redeliver(ctx, targetID, req, resume)
	} else {
		var cancel *cancelControl
		if req.Abort != nil {
			cancel = &cancelControl{requesterID: req.RequesterID, abort: req.Abort}
		}
		err = b.deliverStream(ctx, targetID, req.ReplyTo, req.Stream, cancel, resume)
	}
	// The triggering message's 👀 crosses into this async dispatch by ReplyTo:
	// handleMessage only marks a group turn pending, it never finalizes it.
	b.finishReaction(context.WithoutCancel(ctx), targetID, req.ReplyTo, err == nil)
	return err
}

// redeliver finishes a delivery an earlier attempt left incomplete. The agent
// already ran and its response is persisted, so there is nothing to stream, no
// progress to rebuild, and nothing to cancel: only the chunks the cursor has
// not confirmed yet. The typing indicator still runs, so a resumed tail looks
// like the same reply arriving rather than a second one.
func (b *Bot) redeliver(ctx context.Context, channelID string, req internalchannel.GroupPublishRequest, resume textDelivery) error {
	if strings.TrimSpace(req.Text) == "" {
		return nil
	}
	return b.sendTextChunks(ctx, channelID, req.Text, req.ReplyTo, false, resume)
}
