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
	return b.deliverStream(ctx, targetID, req.ReplyTo, req.Stream)
}
