package qq

import (
	"context"
	"fmt"
	"strings"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
)

func (b *Bot) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	groupID := strings.TrimPrefix(req.PlatformGroupID, "qq:group:")
	if groupID == "" {
		return fmt.Errorf("qq: empty group id")
	}
	if req.Stream == nil {
		return nil
	}
	response, images, streamErr := b.streamResponse(req.Stream.Events, "", groupID, req.ReplyTo, scopeGroup)
	if streamErr != nil {
		if response == "" {
			response = fmt.Sprintf("Agent error: %v", streamErr)
		} else {
			response += fmt.Sprintf("\n\n[Agent error: %v]", streamErr)
		}
	}
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}
	b.sendFinalResponse(groupID, req.ReplyTo, response, scopeGroup)
	for _, img := range images {
		b.sendImage(groupID, req.ReplyTo, img, scopeGroup)
	}
	return nil
}
