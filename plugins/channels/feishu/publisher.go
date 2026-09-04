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
	stream, err := internalchannel.ValidateGroupReplay(ctx, req.Stream)
	if err != nil {
		return err
	}
	if stream == nil {
		return nil
	}
	chatID := strings.TrimPrefix(req.PlatformGroupID, "feishu:")
	rootID := req.PlatformThreadID
	sentMsgID, response, images, files, refs, elapsed, streamErr := b.streamResponseInThread(ctx, stream.Events, chatID, req.ReplyTo, rootID, req.DeliveryID)
	if err := ctx.Err(); err != nil {
		// A lost dispatch lease or shutdown must remain retryable. Do not turn
		// it into a user-visible agent error and incorrectly complete the row.
		return err
	} else if streamErr != nil {
		return fmt.Errorf("feishu: render group replay: %w", streamErr)
	}
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}
	finalResponse := response + elapsedFooter(elapsed)
	if err := b.sendFinalResponseInThreadWithOptions(ctx, chatID, req.ReplyTo, rootID, sentMsgID, finalResponse, refs, true, false, cardStatusCompleted, req.DeliveryID); err != nil {
		logger().Error("Feishu group response delivery failed", "chat_id", chatID, "root_id", rootID, "message_id", req.ReplyTo, "error", err)
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, img := range images {
		if err := b.sendImageInThread(chatID, req.ReplyTo, rootID, img); err != nil {
			return fmt.Errorf("feishu: send response image: %w", err)
		}
	}
	for _, file := range files {
		if err := b.sendFileInThread(chatID, req.ReplyTo, rootID, file); err != nil {
			return fmt.Errorf("feishu: send response file: %w", err)
		}
	}
	return nil
}
