package telegram

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v4"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

// atoiOr converts a string to int, returning fallback on error.
func atoiOr(s string, fallback int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return v
}

func botCommands() []tele.Command {
	return []tele.Command{
		{Text: "start", Description: "Welcome & help"},
		{Text: "new", Description: "Start a fresh session"},
		{Text: "compact", Description: "Compress the current session in place"},
		{Text: "abort", Description: "Cancel the in-progress response"},
		{Text: "model", Description: "List or switch models"},
		{Text: "agent", Description: "List or switch agents"},
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
		b.bot.Handle(cmd, b.guard(func(c tele.Context) error {
			return b.handleSharedCommand(c, cmd)
		}))
	}

	// Telegram-specific /whoami override (includes chat ID in markdown).
	b.bot.Handle("/whoami", b.guard(func(c tele.Context) error {
		if c.Sender() == nil {
			return c.Send("Cannot determine user ID (no sender info).")
		}
		msg := fmt.Sprintf("Your user ID: `%d`\nThis chat ID: `%d`",
			c.Sender().ID, c.Chat().ID)
		return c.Send(msg, tele.ModeMarkdown)
	}))

	b.bot.Handle("/agent", b.guard(func(c tele.Context) error {
		return b.handleAgent(c)
	}))

	b.bot.Handle("/model", b.guard(func(c tele.Context) error {
		return b.handleModel(c)
	}))

	// Handle inline keyboard callbacks for model selection via unique handler.
	// telebot strips the "\fmodel_select|" prefix, so c.Data() = "1", "2", etc.
	b.bot.Handle("\fmodel_select", b.guard(func(c tele.Context) error {
		idxStr := c.Data()
		logger().Debug("model_select callback fired", "data", idxStr, "sender", c.Sender().ID, "chat", c.Chat().ID)
		if err := b.switchModelByIdx(c, atoiOr(idxStr, 0)); err != nil {
			logger().Error("model switch failed", "data", idxStr, "error", err)
			return err
		}
		_ = c.Respond()
		return c.Delete()
	}))

	// Handle pagination for model keyboard.
	b.bot.Handle("\fmodel_page", b.guard(func(c tele.Context) error {
		data := c.Data()
		pageStr, query, _ := strings.Cut(data, "|")
		page, _ := strconv.Atoi(pageStr)

		models := b.modelList(query)
		if err := b.sendModelPage(c, models, page, query, true); err != nil {
			logger().Error("model page failed", "page", page, "error", err)
			return err
		}
		return c.Respond()
	}))

	b.bot.Handle("\fmodel_noop", func(c tele.Context) error {
		return c.Respond()
	})

	// Handle inline keyboard callbacks for agent selection.
	b.bot.Handle("\fagent_select", b.guard(func(c tele.Context) error {
		idxStr := c.Data()
		logger().Debug("agent_select callback fired", "data", idxStr, "sender", c.Sender().ID, "chat", c.Chat().ID)
		if err := b.switchAgentByIdx(c, atoiOr(idxStr, 0)); err != nil {
			logger().Error("agent switch failed", "data", idxStr, "error", err)
			return err
		}
		_ = c.Respond()
		return c.Delete()
	}))

	// Handle pagination for agent keyboard.
	b.bot.Handle("\fagent_page", b.guard(func(c tele.Context) error {
		page, _ := strconv.Atoi(c.Data())

		msg := b.incomingMsg(c, nil)
		agents, currentAgentID, err := b.handler.ListAgents(context.Background(), msg)
		if err != nil {
			return c.Send(fmt.Sprintf("Error: %v", err))
		}
		indexed := channel.IndexAgents(agents)
		if err := b.sendAgentPage(c, indexed, page, currentAgentID, true); err != nil {
			logger().Error("agent page failed", "page", page, "error", err)
			return err
		}
		return c.Respond()
	}))

	b.bot.Handle("\fagent_noop", func(c tele.Context) error {
		return c.Respond()
	})

	b.bot.Handle(tele.OnText, b.guard(func(c tele.Context) error {
		return b.handleText(c)
	}))

	b.bot.Handle(tele.OnPhoto, b.guard(func(c tele.Context) error {
		return b.handlePhoto(c)
	}))

	b.bot.Handle(tele.OnDocument, b.guard(func(c tele.Context) error {
		return b.handleDocument(c)
	}))

	b.bot.Handle(tele.OnCallback, func(c tele.Context) error {
		cb := c.Callback()
		logger().Warn("unmatched callback", "data", cb.Data, "unique", cb.Unique)
		return c.Respond()
	})
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

// modelList returns models optionally filtered by query.
func (b *Bot) modelList(query string) []channel.IndexedModel {
	models := b.handler.ListModels()
	if query == "" {
		return channel.IndexModels(models)
	}
	return channel.FilterModels(models, query)
}

// handleModel processes the /model command with inline keyboard.
func (b *Bot) handleModel(c tele.Context) error {
	args := strings.TrimSpace(c.Message().Payload)
	query := channel.ParseModelArgs(args)

	if query != "" && strings.Contains(query, "/") {
		return b.switchModelByName(c, query)
	}

	models := b.modelList(query)
	if len(models) == 0 {
		if query != "" {
			return c.Send(fmt.Sprintf("No models matching %q.", query))
		}
		return c.Send("No models configured.")
	}
	return b.sendModelKeyboard(c, models)
}

// handleAgent handles the /agent command with inline keyboard.
func (b *Bot) handleAgent(c tele.Context) error {
	args := strings.TrimSpace(c.Message().Payload)

	msg := b.incomingMsg(c, nil)

	// Direct switch by slug.
	if args != "" {
		if err := b.handler.SwitchAgent(context.Background(), msg, args); err != nil {
			return c.Send(fmt.Sprintf("Error switching agent: %v", err))
		}
		logger().Info("agent switched", "agent_id", args)
		return c.Send(fmt.Sprintf("Switched to agent: %s", args))
	}

	agents, currentAgentID, err := b.handler.ListAgents(context.Background(), msg)
	if err != nil {
		return c.Send(fmt.Sprintf("Error listing agents: %v", err))
	}
	if len(agents) == 0 {
		return c.Send("No agents available.")
	}
	return b.sendAgentKeyboard(c, channel.IndexAgents(agents), currentAgentID)
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
	content = append(content, b.imageContent(c, photo.UniqueID, mimeType, data)...)

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

// resolveAssetsDir resolves the per-user assets directory for the message's
// author, or "" when the handler cannot resolve a user root.
func (b *Bot) resolveAssetsDir(c tele.Context) string {
	resolver, ok := b.handler.(channel.UserRootResolver)
	if !ok {
		return ""
	}
	probeMsg := b.incomingMsg(c, nil)
	userRoot, err := resolver.ResolveUserRoot(b.ctx, probeMsg)
	if err != nil {
		logger().Warn("resolve user root failed", "error", err)
		return ""
	}
	return agent.UserAssetsDir(userRoot)
}

// imageContent persists an inbound image message to the user's assets and
// returns the unified attachment blocks. When persistence is unavailable the
// image degrades via the shared inline fallback (inline within the ceiling, else
// a text note) so the message still reaches the agent.
func (b *Bot) imageContent(c tele.Context, uniqueID, mimeType string, data []byte) []ai.ContentBlock {
	fileName := channel.ImageFileName(uniqueID, mimeType)
	if assetsDir := b.resolveAssetsDir(c); assetsDir != "" {
		savedPath, err := b.saveAsset(b.ctx, assetsDir, fileName, data)
		if err == nil {
			return channel.AttachmentReceivedContent(fileName, assetsDir, savedPath, data, isGroup(c))
		}
		logger().Warn("save inbound image failed", "error", err)
	}
	return channel.InlineImageFallback(fileName, mimeType, data, isGroup(c))
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
// storage directory is available; on save failure the turn is never dropped
// (image bytes degrade via the shared inline fallback, other files get a
// placeholder). The bool is false only when the download itself failed and the
// error was already replied to the chat, so nothing can be given to the agent.
func (b *Bot) documentAttachment(c tele.Context, doc *tele.Document, fileName string) ([]ai.ContentBlock, bool) {
	// Resolve the per-user assets directory before downloading.
	assetsDir := b.resolveAssetsDir(c)
	if assetsDir == "" {
		_ = c.Send("Unable to resolve storage directory for this file.")
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

	const maxFileSize = 50 << 20 // 50 MB
	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		logger().Error("read document failed", "error", err)
		_ = c.Send(fmt.Sprintf("Failed to read file: %v", err))
		return nil, false
	}
	if len(data) > maxFileSize {
		_ = c.Send("File too large (max 50 MB).")
		return nil, false
	}

	savedPath, err := b.saveAsset(b.ctx, assetsDir, fileName, data)
	if err != nil {
		logger().Warn("save document failed", "error", err)
		return channel.AttachmentSaveFailureContent(fileName, data, isGroup(c)), true
	}

	logger().Debug("document received", "file_name", fileName, "size", len(data), "path", savedPath)
	return channel.AttachmentReceivedContent(fileName, assetsDir, savedPath, data, isGroup(c)), true
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
