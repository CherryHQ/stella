package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	tele "gopkg.in/telebot.v4"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

func botCommands() []tele.Command {
	return []tele.Command{
		{Text: "start", Description: "Welcome & help"},
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
		b.bot.Handle(cmd, b.guard(true, func(c tele.Context) error {
			return b.handleSharedCommand(c, cmd)
		}))
	}

	// Telegram-specific /whoami override (includes chat ID in markdown).
	b.bot.Handle("/whoami", b.guard(true, func(c tele.Context) error {
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
		return c.Send("No photo found in message.")
	}
	assetMsg, admitErr := b.admitAttachmentSave(c)
	if admitErr != nil {
		return b.rejectAttachment(c, admitErr)
	}

	file, err := b.bot.File(&photo.File)
	if err != nil {
		logger().Error("download photo failed", "error", err)
		return c.Send(fmt.Sprintf("Failed to download photo: %v", err))
	}
	defer func() { _ = file.Close() }()

	const maxPhotoSize = 20 << 20 // 20MB
	data, err := io.ReadAll(io.LimitReader(file, maxPhotoSize+1))
	if err != nil {
		logger().Error("read photo failed", "error", err)
		return c.Send(fmt.Sprintf("Failed to read photo: %v", err))
	}
	if len(data) > maxPhotoSize {
		return c.Send("Photo too large (max 20 MB).")
	}

	mimeType := http.DetectContentType(data)

	var content []ai.ContentBlock
	if caption := c.Message().Caption; caption != "" {
		if isGroup(c) {
			caption = b.stripBotMention(caption)
		}
		content = append(content, ai.TextContent{Text: caption})
	}
	content = append(content, b.imageContent(assetMsg, photo.UniqueID, mimeType, data)...)

	logger().Debug("photo received", "chat_id", c.Chat().ID, "size", len(data), "mime", mimeType)

	msg := b.incomingMsg(c, content)

	// Photos are never commands — pass empty command to HandleIncoming.
	_, _, stream, err := b.handler.HandleIncoming(b.ctx, msg, "", "")
	if err != nil {
		logger().Error("chat failed", "chat_id", c.Chat().ID, "error", err)
		return c.Send(fmt.Sprintf("Session error: %v", err))
	}
	if stream == nil {
		return nil
	}
	return b.handleStream(c, stream)
}

func (b *Bot) rejectAttachment(c tele.Context, err error) error {
	if errors.Is(err, internalchannel.ErrAgentAccessDenied) || errors.Is(err, agentaccess.ErrForbidden) {
		return c.Send("Attachments are not supported in guest chat.")
	}
	return c.Send("Unable to process this attachment.")
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

// imageContent persists an inbound image message to the user's assets and
// returns the unified attachment blocks. When persistence is unavailable the
// image degrades via the shared inline fallback (inline within the ceiling, else
// a text note) so the message still reaches the agent.
func (b *Bot) imageContent(assetMsg channel.IncomingMessage, uniqueID, mimeType string, data []byte) []ai.ContentBlock {
	fileName := channel.ImageFileName(uniqueID, mimeType)
	if assetMsg.Platform != "" {
		savedPath, err := b.saveAsset(b.ctx, assetMsg, fileName, data)
		if err == nil {
			return channel.AttachmentReceivedContent(fileName, savedPath, data)
		}
		logger().Warn("save inbound image failed", "error", err)
	}
	return channel.InlineImageFallback(fileName, mimeType, data)
}

// handleDocument processes incoming document (file) messages. It downloads the
// file, persists it to the user's assets, and passes an Xberg extraction hint to
// the agent. A persistence failure never drops the turn: image documents degrade
// via the shared inline fallback and other files get an explicit placeholder.
func (b *Bot) handleDocument(c tele.Context) error {
	doc := c.Message().Document
	if doc == nil {
		return c.Send("No document found in message.")
	}

	fileName := doc.FileName
	if fileName == "" {
		fileName = doc.FileID
	}

	attachment, ok := b.documentAttachment(c, doc, fileName)
	if !ok {
		// Download failed and there is nothing to give the agent; the error was
		// already replied to the chat.
		return nil
	}

	var content []ai.ContentBlock
	if caption := c.Message().Caption; caption != "" {
		if isGroup(c) {
			caption = b.stripBotMention(caption)
		}
		content = append(content, ai.TextContent{Text: caption})
	}
	content = append(content, attachment...)

	msg := b.incomingMsg(c, content)
	_, _, stream, err := b.handler.HandleIncoming(b.ctx, msg, "", "")
	if err != nil {
		logger().Error("chat failed", "chat_id", c.Chat().ID, "error", err)
		return c.Send(fmt.Sprintf("Session error: %v", err))
	}
	if stream == nil {
		return nil
	}
	return b.handleStream(c, stream)
}

// documentAttachment downloads a Telegram document and returns the content
// blocks to route to the agent. It persists the file to the user's assets when a
// rooted publication is authorized; on save failure the turn is never dropped
// (image bytes degrade via the shared inline fallback, other files get a
// placeholder). The bool is false only when the download itself failed and the
// error was already replied to the chat, so nothing can be given to the agent.
func (b *Bot) documentAttachment(c tele.Context, doc *tele.Document, fileName string) ([]ai.ContentBlock, bool) {
	// Authorize attachment publication before downloading untrusted bytes.
	assetMsg, admitErr := b.admitAttachmentSave(c)
	if admitErr != nil {
		_ = b.rejectAttachment(c, admitErr)
		return nil, false
	}

	// Download the file from Telegram.
	_ = c.Notify(tele.UploadingDocument)
	file, err := b.bot.File(&doc.File)
	if err != nil {
		logger().Error("download document failed", "error", err)
		_ = c.Send(fmt.Sprintf("Failed to download file: %v", err))
		return nil, false
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, channel.MaxInboundAttachmentBytes+1))
	if err != nil {
		logger().Error("read document failed", "error", err)
		_ = c.Send(fmt.Sprintf("Failed to read file: %v", err))
		return nil, false
	}
	if len(data) > channel.MaxInboundAttachmentBytes {
		_ = c.Send("File too large (max 32 MiB).")
		return nil, false
	}

	if assetMsg.Platform == "" {
		return channel.AttachmentSaveFailureContent(fileName, data), true
	}
	savedPath, err := b.saveAsset(b.ctx, assetMsg, fileName, data)
	if err != nil {
		logger().Warn("save document failed", "error", err)
		return channel.AttachmentSaveFailureContent(fileName, data), true
	}

	logger().Debug("document received", "file_name", fileName, "size", len(data), "path", savedPath)
	return channel.AttachmentReceivedContent(fileName, savedPath, data), true
}

// handleStream renders a ChatStream to the Telegram chat.
func (b *Bot) handleStream(c tele.Context, stream *channel.ChatStream) error {
	chatID := c.Chat().ID
	logger().Debug("message received", "chat_id", chatID)

	typingCtx, stopTyping := context.WithCancel(b.ctx)
	go keepTyping(typingCtx, c)

	response, tracker, images, streamErr := b.streamEvents(c, stream.Events)

	stopTyping()

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

	b.sendFinalResponse(c, response, images)
	logger().Debug("response sent", "chat_id", chatID, "response_len", len(response))
	return nil
}
