package telegram

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/channel"
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

func botCommands() []tele.Command {
	return []tele.Command{
		{Text: "start", Description: "Welcome & help"},
		{Text: "new", Description: "Start a new session"},
		{Text: "compact", Description: "Compact session history"},
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
		cmd := cmd
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

	b.bot.Handle(tele.OnCallback, func(c tele.Context) error {
		cb := c.Callback()
		logger().Warn("unmatched callback", "data", cb.Data, "unique", cb.Unique)
		return c.Respond()
	})
}

// handleSharedCommand forwards a shared slash command to the coordinator.
func (b *Bot) handleSharedCommand(c tele.Context, cmd string) error {
	msg := b.incomingMsg(c, nil)
	resp, handled, _, err := b.handler.HandleIncoming(b.ctx, msg, cmd, "")
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
	encoded := base64.StdEncoding.EncodeToString(data)

	var content []ai.ContentBlock
	if caption := c.Message().Caption; caption != "" {
		if isGroup(c) {
			caption = b.stripBotMention(caption)
		}
		content = append(content, ai.TextContent{Text: caption})
	}
	content = append(content, ai.ImageContent{Data: encoded, MimeType: mimeType})

	logger().Debug("photo received", "chat_id", c.Chat().ID, "size", len(data), "mime", mimeType)

	msg := b.incomingMsg(c, content)

	// Photos are never commands — pass empty command to HandleIncoming.
	_, _, stream, err := b.handler.HandleIncoming(b.ctx, msg, "", "")
	if err != nil {
		logger().Error("chat failed", "chat_id", c.Chat().ID, "error", err)
		return c.Send(fmt.Sprintf("Session error: %v", err))
	}
	return b.handleStream(c, stream)
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
