package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v4"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/channel"
)

func (b *Bot) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) error {
	chatID, err := strconv.ParseInt(req.PlatformGroupID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: invalid group id %q: %w", req.PlatformGroupID, err)
	}
	chat := &tele.Chat{ID: chatID}
	opts := &tele.SendOptions{ParseMode: tele.ModeMarkdownV2}
	if req.PlatformThreadID != "" {
		threadID, err := strconv.Atoi(req.PlatformThreadID)
		if err != nil {
			return fmt.Errorf("telegram: invalid thread id %q: %w", req.PlatformThreadID, err)
		}
		opts.ThreadID = threadID
	}
	if req.ReplyTo != "" {
		replyID, err := strconv.Atoi(req.ReplyTo)
		if err != nil {
			return fmt.Errorf("telegram: invalid reply_to %q: %w", req.ReplyTo, err)
		}
		opts.ReplyParams = &tele.ReplyParams{MessageID: replyID, ChatID: chatID, AllowWithoutReply: true}
	}

	response, images, err := collectTelegramResponse(ctx, req.Stream)
	if err != nil {
		if response == "" {
			response = fmt.Sprintf("Agent error: %v", err)
		} else {
			response += fmt.Sprintf("\n\n[Agent error: %v]", err)
		}
	}
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}
	if err := b.sendChunkedMarkdown(chat, response, false, opts); err != nil {
		return err
	}
	for _, img := range images {
		b.sendImageTo(chat, img)
	}
	return nil
}

func collectTelegramResponse(ctx context.Context, stream *channel.ChatStream) (string, []channel.ImageEvent, error) {
	if stream == nil {
		return "", nil, nil
	}
	var sb strings.Builder
	var streamErr error
	var images []channel.ImageEvent
	tracker := newToolTracker()
	for {
		select {
		case evt, ok := <-stream.Events:
			if !ok {
				response := sb.String()
				if tracker.HasHistory() {
					response += tracker.RenderFinal()
				}
				return response, images, streamErr
			}
			if evt.Err != nil {
				streamErr = evt.Err
				continue
			}
			if evt.Image != nil {
				images = append(images, *evt.Image)
				continue
			}
			if evt.ToolUse != nil {
				tracker.Handle(evt.ToolUse)
				continue
			}
			sb.WriteString(evt.Text)
		case <-ctx.Done():
			return sb.String(), images, ctx.Err()
		}
	}
}
