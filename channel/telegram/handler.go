package telegram

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	aitypes "github.com/vaayne/anna/ai/types"
	tele "gopkg.in/telebot.v4"
)

const welcomeMessage = `👋 Hi! I'm Anna — your local AI assistant.

*Commands*
/new — Start a fresh session
/compact — Compress conversation history
/model — Switch between models

Just send me a message to get started.`

func botCommands() []tele.Command {
	return []tele.Command{
		{Text: "start", Description: "Welcome & help"},
		{Text: "new", Description: "Start a new session"},
		{Text: "compact", Description: "Compact session history"},
		{Text: "model", Description: "List or switch models"},
	}
}

func registerCommands(bot *tele.Bot) error {
	return bot.SetCommands(botCommands())
}

func (b *Bot) registerHandlers() {
	b.bot.Handle("/start", b.guard(func(c tele.Context) error {
		return c.Send(welcomeMessage, tele.ModeMarkdown)
	}))

	b.bot.Handle("/new", b.guard(func(c tele.Context) error {
		ch := channelForChat(c)
		info, err := b.pool.RotateSession(ch)
		if err != nil {
			logger().Error("rotate session failed", "channel", ch, "error", err)
			return c.Send(fmt.Sprintf("Error creating new session: %v", err))
		}
		logger().Info("new session created", "session_id", info.ID, "channel", ch)
		return c.Send("New session started.")
	}))

	b.bot.Handle("/compact", b.guard(func(c tele.Context) error {
		sessionID, err := b.resolveSession(c)
		if err != nil {
			return c.Send(fmt.Sprintf("No active session: %v", err))
		}
		_ = c.Notify(tele.Typing)
		summary, err := b.pool.CompactSession(b.ctx, sessionID)
		if err != nil {
			logger().Error("compact session failed", "session_id", sessionID, "error", err)
			return c.Send(fmt.Sprintf("Compaction failed: %v", err))
		}
		logger().Info("session compacted", "session_id", sessionID, "summary_len", len(summary))
		return c.Send("Session compacted.")
	}))

	b.bot.Handle("/model", b.guard(func(c tele.Context) error {
		args := strings.TrimSpace(c.Message().Payload)
		models := b.listFn()

		if args == "" {
			return b.sendModelKeyboard(c, indexModels(models))
		}

		// Numeric arg → direct switch by index.
		if _, err := strconv.Atoi(args); err == nil {
			return b.switchModel(c, models, args)
		}

		// Text arg → filter models by substring match, preserving global indices.
		filtered := filterModels(models, args)
		if len(filtered) == 0 {
			return c.Send(fmt.Sprintf("No models matching %q.", args))
		}
		return b.sendModelKeyboard(c, filtered)
	}))

	// Handle inline keyboard callbacks for model selection via unique handler.
	// telebot strips the "\fmodel_select|" prefix, so c.Data() = "1", "2", etc.
	b.bot.Handle("\fmodel_select", b.guard(func(c tele.Context) error {
		idxStr := c.Data()
		logger().Debug("model_select callback fired", "data", idxStr, "sender", c.Sender().ID, "chat", c.Chat().ID)
		models := b.listFn()
		if err := b.switchModel(c, models, idxStr); err != nil {
			logger().Error("model switch failed", "data", idxStr, "error", err)
			return err
		}
		_ = c.Respond()
		return c.Delete()
	}))

	// Handle pagination for model keyboard.
	// Callback data format: "page" or "page|filter_query".
	b.bot.Handle("\fmodel_page", b.guard(func(c tele.Context) error {
		data := c.Data()
		pageStr, query, _ := strings.Cut(data, "|")
		page, _ := strconv.Atoi(pageStr)

		allModels := b.listFn()
		models := filterModels(allModels, query)
		if err := b.sendModelPage(c, models, page, query, true); err != nil {
			logger().Error("model page failed", "page", page, "error", err)
			return err
		}
		return c.Respond()
	}))

	// No-op handler for the page counter button.
	b.bot.Handle("\fmodel_noop", func(c tele.Context) error {
		return c.Respond()
	})

	b.bot.Handle(tele.OnText, b.guard(func(c tele.Context) error {
		return b.handleText(c)
	}))

	b.bot.Handle(tele.OnPhoto, b.guard(func(c tele.Context) error {
		return b.handlePhoto(c)
	}))

	// Debug: catch-all callback handler for unmatched callbacks.
	b.bot.Handle(tele.OnCallback, func(c tele.Context) error {
		cb := c.Callback()
		logger().Warn("unmatched callback", "data", cb.Data, "unique", cb.Unique)
		return c.Respond()
	})
}

// handleText processes incoming text messages.
func (b *Bot) handleText(c tele.Context) error {
	text := c.Message().Text
	if isGroup(c) {
		text = b.stripBotMention(text)
	}
	return b.handleMessage(c, text)
}

// handlePhoto processes incoming photo messages.
func (b *Bot) handlePhoto(c tele.Context) error {
	photo := c.Message().Photo
	if photo == nil {
		return c.Send("No photo found in message.")
	}

	rc, err := b.bot.File(&photo.File)
	if err != nil {
		logger().Error("download photo failed", "error", err)
		return c.Send(fmt.Sprintf("Failed to download photo: %v", err))
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil {
		logger().Error("read photo failed", "error", err)
		return c.Send(fmt.Sprintf("Failed to read photo: %v", err))
	}

	mimeType := http.DetectContentType(data)
	encoded := base64.StdEncoding.EncodeToString(data)

	content := []aitypes.ContentBlock{
		aitypes.ImageContent{Data: encoded, MimeType: mimeType},
	}
	if caption := c.Message().Caption; caption != "" {
		content = append(content, aitypes.TextContent{Text: caption})
	}

	logger().Debug("photo received", "chat_id", c.Chat().ID, "size", len(data), "mime", mimeType)
	return b.handleMessage(c, content)
}

// handleMessage is the common flow for text and multimodal messages.
// message is string or []aitypes.ContentBlock.
func (b *Bot) handleMessage(c tele.Context, message any) error {
	chatID := c.Chat().ID
	sessionID, err := b.resolveSession(c)
	if err != nil {
		logger().Error("resolve session failed", "chat_id", chatID, "error", err)
		return c.Send(fmt.Sprintf("Session error: %v", err))
	}

	logger().Debug("message received", "chat_id", chatID)

	typingCtx, stopTyping := context.WithCancel(b.ctx)
	go keepTyping(typingCtx, c)

	response, tracker, streamErr := b.streamResponse(c, sessionID, message)

	stopTyping()

	if streamErr != nil {
		logger().Error("agent stream error", "session_id", sessionID, "error", streamErr)
		if response == "" {
			response = fmt.Sprintf("Agent error: %v", streamErr)
		} else {
			response += fmt.Sprintf("\n\n[Agent error: %v]", streamErr)
		}
	}

	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}

	if tracker != nil && tracker.hasHistory() {
		response += tracker.renderFinal()
	}

	b.sendFinalResponse(c, response)
	logger().Debug("response sent", "chat_id", chatID, "response_len", len(response))
	return nil
}
