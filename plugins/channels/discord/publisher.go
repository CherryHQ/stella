package discord

import (
	"context"
	"errors"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

func (b *Bot) Publish(ctx context.Context, req pkgchannel.GroupPublishRequest) error {
	if req.Stream == nil {
		return nil
	}
	stream, err := pkgchannel.ValidateGroupReplay(ctx, req.Stream)
	if err != nil {
		outcome := pkgchannel.DeliveryResultForError(err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = pkgchannel.DeliveryNotSent
		}
		ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		ackErr := req.Stream.Settle(ackCtx, outcome)
		cancel()
		req.Stream.Discard()
		if ackErr != nil {
			return errors.Join(err, ackErr)
		}
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
