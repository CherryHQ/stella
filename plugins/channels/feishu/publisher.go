package feishu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

func (b *Bot) Publish(ctx context.Context, req pkgchannel.GroupPublishRequest) (err error) {
	if req.Stream != nil {
		defer req.Stream.Discard()
	}
	outcome := pkgchannel.EgressDelivered
	defer func() {
		if req.Stream == nil {
			return
		}
		ackCtx, cancelAck := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelAck()
		if ackErr := req.Stream.Ack(ackCtx, outcome); ackErr != nil && err == nil {
			err = ackErr
		}
	}()
	if err := ctx.Err(); err != nil {
		outcome = pkgchannel.EgressDiscarded
		return err
	}
	stream, err := pkgchannel.ValidateGroupReplay(ctx, req.Stream)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = pkgchannel.EgressDiscarded
		} else {
			outcome = pkgchannel.EgressFailed
		}
		return err
	}
	if stream == nil {
		return nil
	}
	chatID := strings.TrimPrefix(req.PlatformGroupID, "feishu:")
	rootID := req.PlatformThreadID
	sentMsgID, response, images, files, refs, elapsed, streamErr := b.streamResponseInThreadChecked(ctx, stream, chatID, req.ReplyTo, rootID, req.DeliveryID)
	if err := ctx.Err(); err != nil {
		// The streaming renderer may already have sent a card update. Cancellation
		// therefore cannot prove that Feishu did not receive the effect.
		outcome = pkgchannel.EgressOutcomeForError(err)
		return err
	} else if streamErr != nil {
		outcome = pkgchannel.EgressOutcomeForError(streamErr)
		return fmt.Errorf("feishu: render group replay: %w", streamErr)
	}
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}
	finalResponse := response + elapsedFooter(elapsed)
	if err := b.sendFinalResponseInThreadChecked(ctx, stream, chatID, req.ReplyTo, rootID, sentMsgID, finalResponse, refs, true, false); err != nil {
		outcome = pkgchannel.EgressOutcomeForError(err)
		logger().Error("Feishu group response delivery failed", "chat_id", chatID, "root_id", rootID, "message_id", req.ReplyTo, "error", err)
		return err
	}
	if err := ctx.Err(); err != nil {
		outcome = pkgchannel.EgressOutcomeForError(err)
		return err
	}
	for _, img := range images {
		if err := b.sendImageInThreadChecked(ctx, stream, chatID, req.ReplyTo, rootID, img); err != nil {
			outcome = pkgchannel.EgressOutcomeForError(err)
			return fmt.Errorf("feishu: send response image: %w", err)
		}
	}
	for _, file := range files {
		if err := b.sendFileInThreadChecked(ctx, stream, chatID, req.ReplyTo, rootID, file); err != nil {
			outcome = pkgchannel.EgressOutcomeForError(err)
			return fmt.Errorf("feishu: send response file: %w", err)
		}
	}
	return nil
}
