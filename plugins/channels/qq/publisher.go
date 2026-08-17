package qq

import (
	"context"
	"fmt"
	"strings"

	"github.com/tencent-connect/botgo/dto"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/channel"
)

func (b *Bot) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) error {
	defer req.Stream.Discard()
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
	var response strings.Builder
	var streamErr error
	for event := range req.Stream.Events {
		if event.Err != nil {
			streamErr = event.Err
			break
		}
		response.WriteString(event.Text)
	}
	text := response.String()
	if streamErr != nil {
		if text == "" {
			text = fmt.Sprintf("Agent error: %v", streamErr)
		} else {
			text += fmt.Sprintf("\n\n[Agent error: %v]", streamErr)
		}
	}
	if strings.TrimSpace(text) == "" {
		text = "(empty response)"
	}
	for i, chunk := range channel.SplitMessage(text, qqMaxMessageLen) {
		if err := req.Stream.CheckOperation(ctx); err != nil {
			return err
		}
		_, err := b.api.PostGroupMessage(ctx, groupID, dto.MessageToCreate{
			Content: chunk, MsgType: dto.TextMsg, MsgID: req.ReplyTo, MsgSeq: uint32(100 + i),
		})
		if err != nil {
			return fmt.Errorf("qq: publish response chunk %d: %w", i+1, err)
		}
	}
	return nil
}
