package discord

import (
	"context"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
)

func (b *Bot) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) error {
	defer req.Stream.Discard()
	targetID := req.PlatformGroupID
	if req.PlatformThreadID != "" {
		targetID = req.PlatformThreadID
	}
	var cancel *cancelControl
	if req.Abort != nil {
		cancel = &cancelControl{requesterID: req.RequesterID, abort: req.Abort}
	}
	err := b.deliverStream(ctx, targetID, req.ReplyTo, req.Stream, cancel)
	// Only explicitly addressed turns opt into reaction lifecycle feedback.
	// Ambient semantic-routing messages must not receive an unsolicited naked
	// terminal reaction when no 👀 acknowledgement was started.
	if req.LifecycleFeedback && err == nil {
		if reactionErr := b.finishReactionChecked(context.WithoutCancel(ctx), req.Stream, targetID, req.ReplyTo, true); reactionErr != nil {
			return reactionErr
		}
	}
	return err
}
