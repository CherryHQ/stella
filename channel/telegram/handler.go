package telegram

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/vaayne/anna/agent/runner"
	aitypes "github.com/vaayne/anna/ai/types"
	"github.com/vaayne/anna/channel"
	tele "gopkg.in/telebot.v4"
)

// atoiOr converts a string to int, returning fallback on error.
func atoiOr(s string, fallback int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return v
}

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
		sessionID, err := b.cmd.New(ch)
		if err != nil {
			logger().Error("rotate session failed", "channel", ch, "error", err)
			return c.Send(fmt.Sprintf("Error creating new session: %v", err))
		}
		logger().Info("new session created", "session_id", sessionID, "channel", ch)
		return c.Send("New session started.")
	}))

	b.bot.Handle("/compact", b.guard(func(c tele.Context) error {
		ch := channelForChat(c)
		_ = c.Notify(tele.Typing)
		summary, err := b.cmd.Compact(b.ctx, ch)
		if err != nil {
			logger().Error("compact session failed", "channel", ch, "error", err)
			return c.Send(fmt.Sprintf("Compaction failed: %v", err))
		}
		logger().Info("session compacted", "channel", ch, "summary_len", len(summary))
		return c.Send("Session compacted.")
	}))

	b.bot.Handle("/model", b.guard(func(c tele.Context) error {
		args := strings.TrimSpace(c.Message().Payload)
		idx, query := channel.ParseModelArgs(args)

		if idx > 0 {
			return b.switchModelByIdx(c, idx)
		}

		models := b.cmd.ModelList(query)
		if len(models) == 0 {
			if query != "" {
				return c.Send(fmt.Sprintf("No models matching %q.", query))
			}
			return c.Send("No models configured.")
		}
		return b.sendModelKeyboard(c, models)
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
	// Callback data format: "page" or "page|filter_query".
	b.bot.Handle("\fmodel_page", b.guard(func(c tele.Context) error {
		data := c.Data()
		pageStr, query, _ := strings.Cut(data, "|")
		page, _ := strconv.Atoi(pageStr)

		models := b.cmd.ModelList(query)
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

	const maxPhotoSize = 20 << 20 // 20MB — Telegram's file size limit
	data, err := io.ReadAll(io.LimitReader(rc, maxPhotoSize+1))
	if err != nil {
		logger().Error("read photo failed", "error", err)
		return c.Send(fmt.Sprintf("Failed to read photo: %v", err))
	}
	if len(data) > maxPhotoSize {
		return c.Send("Photo too large (max 20 MB).")
	}

	mimeType := http.DetectContentType(data)
	encoded := base64.StdEncoding.EncodeToString(data)

	var content []aitypes.ContentBlock
	if caption := c.Message().Caption; caption != "" {
		if isGroup(c) {
			caption = b.stripBotMention(caption)
		}
		content = append(content, aitypes.TextContent{Text: caption})
	}
	content = append(content, aitypes.ImageContent{Data: encoded, MimeType: mimeType})

	logger().Debug("photo received", "chat_id", c.Chat().ID, "size", len(data), "mime", mimeType)
	return b.handleMessage(c, content)
}

// handleMessage is the common flow for text and multimodal messages.
func (b *Bot) handleMessage(c tele.Context, message runner.MessageContent) error {
	chatID := c.Chat().ID
	sessionID, err := b.resolveSession(c)
	if err != nil {
		logger().Error("resolve session failed", "chat_id", chatID, "error", err)
		return c.Send(fmt.Sprintf("Session error: %v", err))
	}

	logger().Debug("message received", "chat_id", chatID)

	typingCtx, stopTyping := context.WithCancel(b.ctx)
	go keepTyping(typingCtx, c)

	response, tracker, images, streamErr := b.streamResponse(c, sessionID, message)

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

	b.sendFinalResponse(c, response, images)
	logger().Debug("response sent", "chat_id", chatID, "response_len", len(response))
	return nil
}
