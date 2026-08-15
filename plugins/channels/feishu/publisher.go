package feishu

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
	chatID := strings.TrimPrefix(req.PlatformGroupID, "feishu:")
	rootID := req.PlatformThreadID
	// A re-delivery carries the already-persisted response instead of a live
	// stream: no streaming card to update, no elapsed timer, and no attachments
	// (they are not persisted). Feishu does not confirm chunks, so it resends
	// the whole response rather than resuming mid-message — at-least-once
	// delivery, but still without ever re-running the agent.
	if req.Stream == nil {
		if strings.TrimSpace(req.Text) == "" {
			return nil
		}
		b.sendFinalResponseInThread(chatID, req.ReplyTo, rootID, "", req.Text, nil, true)
		return nil
	}
	sentMsgID, response, images, files, refs, elapsed, streamErr := b.streamResponseInThread(req.Stream.Events, chatID, req.ReplyTo, rootID)
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
	finalResponse := response + elapsedFooter(elapsed)
	b.sendFinalResponseInThread(chatID, req.ReplyTo, rootID, sentMsgID, finalResponse, refs, true)
	for _, img := range images {
		b.sendImageInThread(chatID, req.ReplyTo, rootID, img)
	}
	for _, file := range files {
		b.sendFileInThread(chatID, req.ReplyTo, rootID, file)
	}
	return nil
}
