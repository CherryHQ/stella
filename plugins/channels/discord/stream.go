package discord

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/channel"
)

const workingMessage = "Working…"

type discordDraft struct {
	bot       *Bot
	channelID string
	messageID string
	last      string
}

func (b *Bot) beginDraft(ctx context.Context, channelID, replyTo string) *discordDraft {
	if b.rest == nil {
		return nil
	}
	message, err := b.rest.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         workingMessage,
		AllowedMentions: noMentions(),
		Reference:       softReference(channelID, replyTo),
	}, discordgo.WithContext(ctx))
	if err != nil {
		logger().Debug("create Discord progress message failed", "channel_id", channelID, "error", err)
		return nil
	}
	if message == nil || message.ID == "" {
		return nil
	}
	return &discordDraft{bot: b, channelID: channelID, messageID: message.ID, last: workingMessage}
}

func (d *discordDraft) edit(ctx context.Context, content string) error {
	if d == nil || content == "" || content == d.last {
		return nil
	}
	edit := discordgo.NewMessageEdit(d.channelID, d.messageID).SetContent(content)
	edit.AllowedMentions = noMentions()
	if _, err := d.bot.rest.ChannelMessageEditComplex(edit, discordgo.WithContext(ctx)); err != nil {
		return err
	}
	d.last = content
	return nil
}

func (d *discordDraft) delete(ctx context.Context) {
	if d == nil {
		return
	}
	if err := d.bot.rest.ChannelMessageDelete(d.channelID, d.messageID, discordgo.WithContext(ctx)); err != nil {
		logger().Debug("delete Discord progress message failed", "channel_id", d.channelID, "message_id", d.messageID, "error", err)
	}
}

func (b *Bot) deliverStream(ctx context.Context, channelID, replyTo string, stream *channel.ChatStream) error {
	draft := b.beginDraft(ctx, channelID, replyTo)
	text, images, files, streamErr := collectResponse(ctx, stream, func(text string, tools *channel.ToolTracker) {
		if draft == nil {
			return
		}
		if err := draft.edit(ctx, buildDraftDisplay(text, tools)); err != nil {
			logger().Debug("edit Discord progress message failed", "channel_id", channelID, "message_id", draft.messageID, "error", err)
		}
	})
	if errors.Is(streamErr, context.Canceled) {
		draft.delete(context.WithoutCancel(ctx))
		if err := ctx.Err(); err != nil {
			// Publisher lease loss and shutdown must remain retryable. A DM /abort
			// reports cancellation only on the stream while deliveryCtx stays live.
			return err
		}
		return nil
	}
	if streamErr != nil {
		logger().Warn("Discord agent stream failed", "channel_id", channelID, "error", streamErr)
		if text != "" {
			text += "\n\n"
		}
		text += "Stella couldn't complete this response. Please try again."
	}
	if strings.TrimSpace(text) == "" && len(images) == 0 && len(files) == 0 {
		text = "(empty response)"
	}

	if draft != nil && len(text) <= maxMessageLength && len(images) == 0 && len(files) == 0 {
		if err := draft.edit(ctx, text); err == nil {
			return nil
		}
	}
	if text != "" {
		if err := b.sendText(ctx, channelID, text, replyTo); err != nil {
			_ = draft.edit(ctx, "⚠️ Discord delivery failed; Stella will retry.")
			return err
		}
	}
	for _, image := range images {
		if err := b.sendImage(ctx, channelID, image); err != nil {
			_ = draft.edit(ctx, "⚠️ Discord delivery failed; Stella will retry.")
			return err
		}
	}
	for _, file := range files {
		if err := b.sendFile(ctx, channelID, file); err != nil {
			_ = draft.edit(ctx, "⚠️ Discord delivery failed; Stella will retry.")
			return err
		}
	}
	draft.delete(ctx)
	return nil
}

func collectResponse(ctx context.Context, stream *channel.ChatStream, onProgress func(string, *channel.ToolTracker)) (string, []channel.ImageEvent, []channel.FileEvent, error) {
	var text strings.Builder
	var images []channel.ImageEvent
	var files []channel.FileEvent
	var streamErr error
	tools := &channel.ToolTracker{}
	ticker := time.NewTicker(streamEditInterval)
	defer ticker.Stop()
	dirty := false
	for {
		select {
		case <-ctx.Done():
			return text.String(), images, files, ctx.Err()
		case <-ticker.C:
			if dirty && onProgress != nil {
				onProgress(text.String(), tools)
				dirty = false
			}
		case evt, ok := <-stream.Events:
			if !ok {
				return text.String(), images, files, streamErr
			}
			if evt.Err != nil {
				streamErr = evt.Err
			}
			if evt.Text != "" {
				text.WriteString(evt.Text)
				dirty = true
			}
			if evt.ToolUse != nil && tools.Handle(evt.ToolUse) {
				dirty = true
			}
			if evt.Image != nil {
				images = append(images, *evt.Image)
			}
			if evt.File != nil {
				files = append(files, *evt.File)
			}
		}
	}
}

func buildDraftDisplay(text string, tools *channel.ToolTracker) string {
	toolSection := ""
	if tools != nil {
		toolSection = strings.TrimSpace(tools.Render())
		toolSection = truncateTail(toolSection, 800)
	}
	suffix := "▌"
	if toolSection != "" {
		suffix = "\n\n" + toolSection + "\n▌"
	}
	available := maxMessageLength - len(suffix)
	if available < 0 {
		return truncateTail(suffix, maxMessageLength)
	}
	text = truncateTail(text, available)
	if text == "" && toolSection == "" {
		return workingMessage
	}
	return text + suffix
}

func truncateTail(text string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(text) <= maxLen {
		return text
	}
	prefix := "…\n"
	if maxLen <= len(prefix) {
		start := len(text) - maxLen
		for start < len(text) && !utf8.RuneStart(text[start]) {
			start++
		}
		return text[start:]
	}
	start := len(text) - (maxLen - len(prefix))
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return prefix + text[start:]
}
