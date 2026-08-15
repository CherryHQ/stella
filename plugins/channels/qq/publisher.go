package qq

import (
	"context"
	"fmt"
	"strings"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/channel"
)

func (b *Bot) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	groupID := strings.TrimPrefix(req.PlatformGroupID, "qq:group:")
	if groupID == "" {
		return fmt.Errorf("qq: empty group id")
	}
	// A re-delivery carries the already-persisted response instead of a live
	// stream. QQ does not confirm chunks, so it resends the whole response from
	// the start rather than resuming mid-message — at-least-once delivery, but
	// still without ever re-running the agent.
	response, images := req.Text, []channel.ImageEvent(nil)
	if req.Stream != nil {
		var streamErr error
		response, images, streamErr = b.streamResponse(req.Stream.Events, "", groupID, req.ReplyTo, scopeGroup)
		if streamErr != nil {
			if response == "" {
				response = fmt.Sprintf("Agent error: %v", streamErr)
			} else {
				response += fmt.Sprintf("\n\n[Agent error: %v]", streamErr)
			}
		}
	}
	if strings.TrimSpace(response) == "" {
		if req.Stream == nil {
			return nil
		}
		response = "(empty response)"
	}
	b.sendFinalResponse(groupID, req.ReplyTo, response, scopeGroup)
	for _, img := range images {
		b.sendImage(groupID, req.ReplyTo, img, scopeGroup)
	}
	return nil
}
