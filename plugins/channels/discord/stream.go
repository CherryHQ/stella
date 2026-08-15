package discord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/channel"
)

const workingMessage = "Working…"

type discordDraft struct {
	bot         *Bot
	channelID   string
	messageID   string
	last        string
	cancelToken string
}

// cancelButtonComponents attaches a single Danger "Cancel" button, or nil if
// cancel offers no abort — nil produces no Components field, so
// ChannelMessageSendComplex sends a plain message exactly as before this
// feature existed.
func (b *Bot) cancelButtonComponents(cancel *cancelControl) (string, []discordgo.MessageComponent) {
	if cancel == nil || cancel.abort == nil || b.cancels == nil {
		return "", nil
	}
	token := b.cancels.register(cancel.requesterID, cancel.abort)
	return token, []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Cancel", Style: discordgo.DangerButton, CustomID: cancelCustomIDPrefix + token},
		}},
	}
}

func (b *Bot) beginDraft(ctx context.Context, channelID, replyTo string, cancel *cancelControl) *discordDraft {
	if b.rest == nil {
		return nil
	}
	token, components := b.cancelButtonComponents(cancel)
	message, err := b.rest.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         workingMessage,
		AllowedMentions: noMentions(),
		Reference:       softReference(channelID, replyTo),
		Components:      components,
	}, discordgo.WithContext(ctx))
	if err != nil {
		logger().Debug("create Discord progress message failed", "channel_id", channelID, "error", err)
		b.unregisterCancel(token)
		return nil
	}
	if message == nil || message.ID == "" {
		b.unregisterCancel(token)
		return nil
	}
	return &discordDraft{bot: b, channelID: channelID, messageID: message.ID, last: workingMessage, cancelToken: token}
}

// edit updates the draft's visible progress text. It never touches
// Components: the Discord API leaves a message's existing components alone
// when an edit omits the field, so an in-flight turn's Cancel button survives
// every progress tick without being re-sent.
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

// finalize edits the draft into its terminal text and clears any Cancel
// button. It always unregisters the draft's cancel token, even on an edit
// error, so a stale Cancel button an edit failure left clickable resolves to
// "already ended" instead of silently re-arming a finished turn.
func (d *discordDraft) finalize(ctx context.Context, content string) error {
	if d == nil {
		return nil
	}
	empty := []discordgo.MessageComponent{}
	edit := discordgo.NewMessageEdit(d.channelID, d.messageID).SetContent(content)
	edit.AllowedMentions = noMentions()
	edit.Components = &empty
	_, err := d.bot.rest.ChannelMessageEditComplex(edit, discordgo.WithContext(ctx))
	d.bot.unregisterCancel(d.cancelToken)
	d.cancelToken = ""
	if err == nil {
		d.last = content
	}
	return err
}

func (d *discordDraft) delete(ctx context.Context) {
	if d == nil {
		return
	}
	if err := d.bot.rest.ChannelMessageDelete(d.channelID, d.messageID, discordgo.WithContext(ctx)); err != nil {
		logger().Debug("delete Discord progress message failed", "channel_id", d.channelID, "message_id", d.messageID, "error", err)
	}
	d.bot.unregisterCancel(d.cancelToken)
	d.cancelToken = ""
}

// deliverStream renders a live agent turn into the channel. resume carries the
// durable delivery cursor for a group dispatch (zero value for a direct
// message, which has nothing to resume).
func (b *Bot) deliverStream(ctx context.Context, channelID, replyTo string, stream *channel.ChatStream, cancel *cancelControl, resume textDelivery) error {
	draft := b.beginDraft(ctx, channelID, replyTo, cancel)
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

	// The draft is scratch state, never a delivered chunk: it either becomes the
	// whole short reply — chunk 0 of 1 — or it is deleted. A retry re-delivers
	// from the durable cursor, so a "will retry" draft left on screen would
	// outlive the attempt that fixed it and read as a permanent failure.
	if draft != nil && utf8.RuneCountInString(text) <= maxMessageLength && len(images) == 0 && len(files) == 0 {
		if err := draft.finalize(ctx, text); err == nil {
			if err := resume.delivered(ctx, 1); err != nil {
				draft.delete(context.WithoutCancel(ctx))
				return err
			}
			return nil
		}
	}
	if text != "" {
		if err := b.sendTextChunks(ctx, channelID, text, replyTo, false, resume); err != nil {
			draft.delete(context.WithoutCancel(ctx))
			return err
		}
	}
	b.sendMedia(ctx, channelID, images, files)
	draft.delete(ctx)
	return nil
}

// sendMedia uploads the turn's attachments after its text is delivered. A
// failed upload is reported, never returned: images and files are not
// persisted, so failing the publish here would requeue a dispatch whose only
// remaining recovery is re-running the agent — the one thing durable delivery
// exists to prevent. Ceiling: a failed attachment is lost. Persist attachments
// as durable media artifacts if that ever costs more than a re-run.
func (b *Bot) sendMedia(ctx context.Context, channelID string, images []channel.ImageEvent, files []channel.FileEvent) {
	failed := 0
	for _, image := range images {
		if err := b.sendImage(ctx, channelID, image); err != nil {
			logger().Warn("Discord image upload failed after the response text was delivered", "channel_id", channelID, "error", err)
			failed++
		}
	}
	for _, file := range files {
		if err := b.sendFile(ctx, channelID, file); err != nil {
			logger().Warn("Discord file upload failed after the response text was delivered", "channel_id", channelID, "file", file.Name, "error", err)
			failed++
		}
	}
	if failed == 0 {
		return
	}
	notice := fmt.Sprintf("⚠️ %d attachment(s) could not be uploaded to Discord.", failed)
	if err := b.sendText(context.WithoutCancel(ctx), channelID, notice, ""); err != nil {
		logger().Warn("Discord attachment failure notice could not be sent", "channel_id", channelID, "error", err)
	}
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
	available := maxMessageLength - utf8.RuneCountInString(suffix)
	if available < 0 {
		return truncateTail(suffix, maxMessageLength)
	}
	text = truncateTail(text, available)
	if text == "" && toolSection == "" {
		return workingMessage
	}
	return text + suffix
}

// truncateTail keeps the last maxRunes Unicode runes of text, prefixing an
// ellipsis marker when it truncates. Rune-counted so it stays under Discord's
// 2000-character limit for multibyte text (e.g. Chinese) and never splits a
// rune's UTF-8 bytes.
func truncateTail(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	total := utf8.RuneCountInString(text)
	if total <= maxRunes {
		return text
	}
	const prefix = "…\n"
	prefixRunes := utf8.RuneCountInString(prefix)
	if maxRunes <= prefixRunes {
		return lastNRunes(text, maxRunes)
	}
	return prefix + lastNRunes(text, maxRunes-prefixRunes)
}

// lastNRunes returns the last n Unicode runes of s.
func lastNRunes(s string, n int) string {
	total := utf8.RuneCountInString(s)
	if total <= n {
		return s
	}
	drop := total - n
	count := 0
	for i := range s {
		if count == drop {
			return s[i:]
		}
		count++
	}
	return ""
}
