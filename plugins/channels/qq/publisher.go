package qq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tencent-connect/botgo/dto"

	"github.com/CherryHQ/stella/pkg/channel"
)

func (b *Bot) Publish(ctx context.Context, req channel.GroupPublishRequest) (err error) {
	if req.Stream == nil {
		return nil
	}
	defer req.Stream.Discard()
	outcome := channel.DeliverySent
	defer func() {
		ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if ackErr := req.Stream.Settle(ackCtx, outcome); ackErr != nil && err == nil {
			err = ackErr
		}
	}()
	if err := ctx.Err(); err != nil {
		outcome = channel.DeliveryNotSent
		return err
	}
	groupID := strings.TrimPrefix(req.PlatformGroupID, "qq:group:")
	if groupID == "" {
		outcome = channel.DeliveryNotSent
		return fmt.Errorf("qq: empty group id")
	}
	stream, err := channel.ValidateGroupReplay(ctx, req.Stream)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = channel.DeliveryNotSent
		} else {
			outcome = channel.DeliveryResultForError(err)
		}
		return err
	}
	if stream == nil {
		return nil
	}
	response, err := qqGroupReplay(stream)
	if err != nil {
		return err
	}
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}
	if b.api == nil {
		outcome = channel.DeliveryNotSent
		return fmt.Errorf("qq: group API unavailable")
	}
	if err := stream.AuthorizeSend(ctx, channel.SendOutput); err != nil {
		outcome = channel.DeliveryNotSent
		return err
	}
	sent := false
	for i, chunk := range channel.SplitMessage(response, qqMaxMessageLen) {
		if err := stream.AuthorizeSend(ctx, channel.SendOutput); err != nil {
			if sent {
				outcome = channel.DeliveryResultForError(err)
			} else {
				outcome = channel.DeliveryNotSent
			}
			return err
		}
		if _, err := b.api.PostGroupMessage(ctx, groupID, dto.MessageToCreate{
			Content: chunk,
			MsgType: dto.TextMsg,
			MsgID:   req.ReplyTo,
			MsgSeq:  uint32(i + 1),
		}); err != nil {
			outcome = channel.DeliveryResultForError(err)
			return fmt.Errorf("qq: send group response chunk %d: %w", i+1, err)
		}
		sent = true
	}
	return nil
}

// qqGroupReplay renders every replayed event into one complete textual
// delivery. QQ's group API has no binary upload endpoint, so media is made
// explicit instead of silently disappearing; rich media is the upgrade path.
func qqGroupReplay(stream *channel.ChatStream) (string, error) {
	var text strings.Builder
	tools := channel.ToolTracker{}
	for event := range stream.Events {
		if event.Err != nil {
			return "", fmt.Errorf("qq: render group replay: %w", event.Err)
		}
		text.WriteString(event.Text)
		if event.ToolUse != nil {
			tools.Handle(event.ToolUse)
		}
		if event.Image != nil {
			fmt.Fprintf(&text, "\n\n[Image: %s]", event.Image.MimeType)
		}
		if event.File != nil {
			name := event.File.Name
			if name == "" {
				name = event.File.Path
			}
			fmt.Fprintf(&text, "\n\n[File: %s]", name)
		}
	}
	if tools.HasHistory() {
		text.WriteString(tools.RenderFinal())
	}
	return text.String(), nil
}
