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
	if req.Stream == nil {
		return nil
	}
	chatID := strings.TrimPrefix(req.PlatformGroupID, "feishu:")
	rootID := req.PlatformThreadID
	cancelControl := &cancelControl{requesterID: req.RequesterID, abort: req.Abort}
	sentMsgID, response, images, files, refs, elapsed, streamErr := b.streamResponseInThread(ctx, req.Stream.Events, chatID, req.ReplyTo, rootID, cancelControl)
	if cancelControl.wasCancelled() {
		response = "⏹️ Cancelled."
		images = nil
		files = nil
	} else if err := ctx.Err(); err != nil {
		// A lost dispatch lease or shutdown must remain retryable. Do not turn
		// it into a user-visible agent error and incorrectly complete the row.
		return err
	} else if streamErr != nil {
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
	if err := b.sendFinalResponseInThread(ctx, chatID, req.ReplyTo, rootID, sentMsgID, finalResponse, refs, true, req.FinalAttempt); err != nil {
		logger().Error("Feishu group response delivery failed", "chat_id", chatID, "root_id", rootID, "message_id", req.ReplyTo, "error", err)
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, img := range images {
		b.sendImageInThread(chatID, req.ReplyTo, rootID, img)
	}
	for _, file := range files {
		b.sendFileInThread(chatID, req.ReplyTo, rootID, file)
	}
	return nil
}
