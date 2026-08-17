package telegram

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	tele "gopkg.in/telebot.v4"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

func botCommands() []tele.Command {
	return []tele.Command{
		{Text: "start", Description: "Welcome & help"},
		{Text: "help", Description: "Show available commands"},
		{Text: "new", Description: "Start a fresh session"},
		{Text: "compact", Description: "Compress the current session in place"},
		{Text: "abort", Description: "Cancel the in-progress response"},
		{Text: "whoami", Description: "Show your user ID"},
	}
}

func registerCommands(bot *tele.Bot) error {
	return bot.SetCommands(botCommands())
}

func (b *Bot) registerHandlers() {
	// Register shared slash commands explicitly so Telegram's command list and
	// handler table stay aligned. /whoami keeps a Telegram-specific override.
	for _, cmd := range []string{"/start", "/help", "/new", "/compact", "/abort"} {
		b.bot.Handle(cmd, b.guard(false, func(c tele.Context) error {
			return b.handleSharedCommand(c, cmd)
		}))
	}

	// Telegram-specific /whoami override (includes chat ID in markdown).
	b.bot.Handle("/whoami", b.guard(false, func(c tele.Context) error {
		if c.Sender() == nil {
			return c.Send("Cannot determine user ID (no sender info).")
		}
		msg := fmt.Sprintf("Your user ID: `%d`\nThis chat ID: `%d`",
			c.Sender().ID, c.Chat().ID)
		return c.Send(msg, tele.ModeMarkdown)
	}))

	b.bot.Handle(tele.OnText, b.guard(false, func(c tele.Context) error {
		return b.handleText(c)
	}))

	b.bot.Handle(tele.OnPhoto, b.guard(false, func(c tele.Context) error {
		return b.handlePhoto(c)
	}))

	b.bot.Handle(tele.OnDocument, b.guard(false, func(c tele.Context) error {
		return b.handleDocument(c)
	}))

	b.bot.Handle(tele.OnAudio, b.guard(false, func(c tele.Context) error {
		if c.Message().Audio == nil {
			return errors.New("telegram: audio metadata is missing")
		}
		return b.handleMediaAttachment(c, &c.Message().Audio.File, c.Message().Audio.FileName, "audio")
	}))
	b.bot.Handle(tele.OnVideo, b.guard(false, func(c tele.Context) error {
		if c.Message().Video == nil {
			return errors.New("telegram: video metadata is missing")
		}
		return b.handleMediaAttachment(c, &c.Message().Video.File, c.Message().Video.FileName, "video")
	}))
	b.bot.Handle(tele.OnVoice, b.guard(false, func(c tele.Context) error {
		if c.Message().Voice == nil {
			return errors.New("telegram: voice metadata is missing")
		}
		return b.handleMediaAttachment(c, &c.Message().Voice.File, "", "voice")
	}))
	b.bot.Handle(tele.OnVideoNote, b.guard(false, func(c tele.Context) error {
		if c.Message().VideoNote == nil {
			return errors.New("telegram: video note metadata is missing")
		}
		return b.handleMediaAttachment(c, &c.Message().VideoNote.File, "", "video-note")
	}))
	b.bot.Handle(tele.OnAnimation, b.guard(false, func(c tele.Context) error {
		if c.Message().Animation == nil {
			return errors.New("telegram: animation metadata is missing")
		}
		return b.handleMediaAttachment(c, &c.Message().Animation.File, c.Message().Animation.FileName, "animation")
	}))
	b.bot.Handle(tele.OnSticker, b.guard(false, func(c tele.Context) error {
		if c.Message().Sticker == nil {
			return errors.New("telegram: sticker metadata is missing")
		}
		return b.handleMediaAttachment(c, &c.Message().Sticker.File, "", "sticker")
	}))

	b.bot.Handle(tele.OnCallback, b.guard(true, func(c tele.Context) error {
		cb := c.Callback()
		logger().Warn("unmatched callback", "data", cb.Data, "unique", cb.Unique)
		return c.Respond()
	}))
}

// handleSharedCommand forwards a shared slash command to the coordinator,
// including its argument. Telegram splits a message into command and payload,
// so dropping the payload would make every argument-taking shared command
// arrive bare and unaddressable.
func (b *Bot) handleSharedCommand(c tele.Context, cmd string) error {
	msg := b.incomingMsg(c, nil)
	args := strings.TrimSpace(c.Message().Payload)
	resp, handled, _, err := b.handler.HandleIncoming(b.ctx, msg, cmd, args)
	if err != nil {
		return c.Send(fmt.Sprintf("Error: %v", err))
	}
	if handled {
		return c.Send(resp)
	}
	return nil
}

// handleText processes incoming text messages.
func (b *Bot) handleText(c tele.Context) error {
	text := c.Message().Text
	if isGroup(c) {
		text = b.stripBotMention(text)
	}

	msg := b.incomingMsg(c, channel.TextContent(text))

	// Parse command if present.
	var cmd, args string
	if fields := strings.Fields(text); len(fields) > 0 {
		cmd = fields[0]
		args = strings.TrimSpace(strings.TrimPrefix(text, cmd))
	}

	// Single resolution: try command, then fall through to chat.
	resp, handled, stream, err := b.handler.HandleIncoming(b.ctx, msg, cmd, args)
	if err != nil {
		return c.Send(fmt.Sprintf("Error: %v", err))
	}
	if handled {
		return c.Send(resp)
	}
	if stream == nil {
		return nil
	}

	return b.handleStream(c, stream)
}

// handlePhoto processes incoming photo messages.
func (b *Bot) handlePhoto(c tele.Context) error {
	photo := c.Message().Photo
	if photo == nil {
		return errors.New("telegram: photo metadata is missing")
	}
	return b.handleMediaAttachment(c, &photo.File, "", "photo")
}

func (b *Bot) rejectAttachment(c tele.Context, err error) error {
	// Returning an error rejects the update without exposing a visible Telegram
	// reply or reaction before immutable storage and FIFO/quota admission.
	return fmt.Errorf("telegram: reject attachment from chat %d: %w", c.Chat().ID, err)
}

// admitAttachmentSave authorizes rooted attachment publication before the
// plugin downloads untrusted bytes.
func (b *Bot) admitAttachmentSave(c tele.Context) (channel.IncomingMessage, error) {
	resolver, ok := b.handler.(channel.AssetSaveAdmitter)
	if !ok {
		return channel.IncomingMessage{}, errors.New("asset save admitter unavailable")
	}
	probeMsg := b.incomingMsg(c, nil)
	err := resolver.AdmitAssetSave(b.ctx, probeMsg)
	if err != nil {
		logger().Warn("admit attachment save failed", "error", err)
		return channel.IncomingMessage{}, err
	}
	return probeMsg, nil
}

// handleDocument processes incoming document (file) messages.
func (b *Bot) handleDocument(c tele.Context) error {
	doc := c.Message().Document
	if doc == nil {
		return errors.New("telegram: document metadata is missing")
	}
	return b.handleMediaAttachment(c, &doc.File, doc.FileName, "document")
}

// handleMediaAttachment is the common fail-closed acceptance path for every
// Telegram media event. No typing indicator, reaction, or reply is emitted until
// the bytes are durably stored and attachment/FIFO quota admission succeeds.
func (b *Bot) handleMediaAttachment(c tele.Context, media *tele.File, fileName, kind string) error {
	// Authorize attachment publication before downloading untrusted bytes.
	assetMsg, admitErr := b.admitAttachmentSave(c)
	if admitErr != nil {
		return b.rejectAttachment(c, admitErr)
	}
	file, err := b.bot.File(media)
	if err != nil {
		return fmt.Errorf("telegram: download %s: %w", kind, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, channel.MaxInboundAttachmentBytes+1))
	if err != nil {
		return fmt.Errorf("telegram: read %s: %w", kind, err)
	}
	if len(data) > channel.MaxInboundAttachmentBytes {
		return fmt.Errorf("telegram: %s exceeds %d bytes", kind, channel.MaxInboundAttachmentBytes)
	}
	if fileName == "" {
		fileName = media.UniqueID
		if fileName == "" {
			fileName = media.FileID
		}
		if fileName == "" {
			fileName = kind
		}
		if strings.HasPrefix(http.DetectContentType(data), "image/") {
			fileName = channel.ImageFileName(fileName, http.DetectContentType(data))
		}
	}
	savedPath, err := b.saveAsset(b.ctx, assetMsg, fileName, data)
	if err != nil {
		return fmt.Errorf("telegram: persist %s before admission: %w", kind, err)
	}
	content := channel.AttachmentReceivedContent(fileName, savedPath, data)
	if caption := c.Message().Caption; caption != "" {
		if isGroup(c) {
			caption = b.stripBotMention(caption)
		}
		content = append([]ai.ContentBlock{ai.TextContent{Text: caption}}, content...)
	}
	msg := b.incomingMsg(c, content)
	admitter, ok := b.handler.(channel.AttachmentAdmitter)
	if !ok {
		return errors.New("telegram: durable attachment admission unavailable")
	}
	if err := admitter.AdmitAttachments(b.ctx, msg); err != nil {
		return fmt.Errorf("telegram: durably admit %s: %w", kind, err)
	}
	_, _, stream, err := b.handler.HandleIncoming(b.ctx, msg, "", "")
	if err != nil {
		return fmt.Errorf("telegram: handle admitted %s: %w", kind, err)
	}
	if stream == nil {
		return nil
	}
	return b.handleStream(c, stream)
}

// handleStream renders a ChatStream to the Telegram chat.
func (b *Bot) handleStream(c tele.Context, stream *channel.ChatStream) error {
	defer stream.Discard()
	chatID := c.Chat().ID
	logger().Debug("message received", "chat_id", chatID)

	// Acknowledge the turn here rather than on inbound: reaching this point is
	// what guarantees a terminal reaction follows. Group turns are served by
	// Publish instead and carry their own lifecycle.
	reactChat, reactMsg := reactionTarget(c)
	if err := stream.CheckOperation(b.ctx); err != nil {
		return err
	}
	if err := b.react(reactChat, reactMsg, reactionReceived); err != nil {
		return err
	}
	if err := stream.CheckOperation(b.ctx); err != nil {
		return err
	}
	if err := c.Notify(tele.Typing); err != nil {
		return err
	}

	response, tracker, images, streamErr := b.streamEvents(c, stream)

	if streamErr != nil {
		logger().Error("agent stream error", "session_id", stream.SessionID, "error", streamErr)
		if response == "" {
			response = fmt.Sprintf("Agent error: %v", streamErr)
		} else {
			response += fmt.Sprintf("\n\n[Agent error: %v]", streamErr)
		}
	}

	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}

	if tracker != nil && tracker.HasHistory() {
		response += tracker.RenderFinal()
	}

	if streamErr != nil {
		return streamErr
	}
	if err := b.sendFinalResponse(b.ctx, stream, c, response, images); err != nil {
		return err
	}
	if err := stream.CheckOperation(b.ctx); err != nil {
		return err
	}
	if err := b.finishReaction(reactChat, reactMsg, true); err != nil {
		return err
	}
	logger().Debug("response sent", "chat_id", chatID, "response_len", len(response))
	return nil
}
