package qq

import (
	"context"
	"fmt"
	"strings"

	"github.com/tencent-connect/botgo/dto"

	"github.com/CherryHQ/stella/pkg/channel"
)

func (b *Bot) Publish(ctx context.Context, req channel.GroupPublishRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	groupID := strings.TrimPrefix(req.PlatformGroupID, "qq:group:")
	if groupID == "" {
		return fmt.Errorf("qq: empty group id")
	}
	stream, err := channel.ValidateGroupReplay(ctx, req.Stream)
	if err != nil {
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
		return fmt.Errorf("qq: group API unavailable")
	}
	for i, chunk := range channel.SplitMessage(response, qqMaxMessageLen) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := b.api.PostGroupMessage(ctx, groupID, dto.MessageToCreate{
			Content: chunk,
			MsgType: dto.TextMsg,
			MsgID:   req.ReplyTo,
			MsgSeq:  uint32(i + 1),
		}); err != nil {
			return fmt.Errorf("qq: send group response chunk %d: %w", i+1, err)
		}
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
