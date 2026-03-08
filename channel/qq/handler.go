package qq

import (
	"context"
	"fmt"
	"strings"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/event"
)

// c2cMessageHandler returns a handler for private (C2C) messages.
func (b *Bot) c2cMessageHandler() event.C2CMessageEventHandler {
	return func(_ *dto.WSPayload, data *dto.WSC2CMessageData) error {
		msg := (*dto.Message)(data)
		authorID := msg.Author.ID
		if !b.isAllowed(authorID) {
			logger().Warn("unauthorized c2c access", "user_id", authorID)
			return nil
		}

		text := strings.TrimSpace(msg.Content)
		if text == "" {
			return nil
		}

		ch := channelForC2C(authorID)

		if handled := b.handleCommand(text, ch, func(reply string) {
			b.replyC2C(b.ctx, authorID, msg.ID, reply)
		}); handled {
			return nil
		}

		b.handleMessage(ch, authorID, msg.ID, text, scopeC2C)
		return nil
	}
}

// groupATMessageHandler returns a handler for group @mention messages.
func (b *Bot) groupATMessageHandler() event.GroupATMessageEventHandler {
	return func(_ *dto.WSPayload, data *dto.WSGroupATMessageData) error {
		msg := (*dto.Message)(data)
		authorID := msg.Author.ID
		groupID := msg.GroupID
		if !b.isAllowed(authorID) {
			logger().Warn("unauthorized group access", "user_id", authorID, "group_id", groupID)
			return nil
		}

		if !b.shouldRespondInGroup() {
			return nil
		}

		text := strings.TrimSpace(msg.Content)
		if text == "" {
			return nil
		}

		ch := channelForGroup(groupID)

		if handled := b.handleCommand(text, ch, func(reply string) {
			b.replyGroup(b.ctx, groupID, msg.ID, reply)
		}); handled {
			return nil
		}

		b.handleMessage(ch, groupID, msg.ID, text, scopeGroup)
		return nil
	}
}

// messageScope indicates whether a message is from a C2C or group context.
type messageScope int

const (
	scopeC2C messageScope = iota
	scopeGroup
)

// handleMessage processes an incoming text message by streaming the agent response.
func (b *Bot) handleMessage(ch, targetID, msgID, text string, scope messageScope) {
	sessionID, err := b.resolveSession(ch)
	if err != nil {
		logger().Error("resolve session failed", "channel", ch, "error", err)
		b.sendReply(targetID, msgID, fmt.Sprintf("Session error: %v", err), scope)
		return
	}

	logger().Debug("message received", "channel", ch, "text_len", len(text))

	response, streamErr := b.streamResponse(targetID, msgID, sessionID, text, scope)

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

	b.sendFinalResponse(targetID, msgID, response, scope)
	logger().Debug("response sent", "channel", ch, "response_len", len(response))
}

// handleCommand checks if text is a bot command and handles it.
// Returns true if the text was a command.
func (b *Bot) handleCommand(text, ch string, reply func(string)) bool {
	cmd := strings.ToLower(strings.Fields(text)[0])

	switch cmd {
	case "/start", "/help":
		reply(welcomeMessage)
		return true

	case "/new":
		info, err := b.pool.RotateSession(ch)
		if err != nil {
			logger().Error("rotate session failed", "channel", ch, "error", err)
			reply(fmt.Sprintf("Error creating new session: %v", err))
			return true
		}
		logger().Info("new session created", "session_id", info.ID, "channel", ch)
		reply("New session started.")
		return true

	case "/compact":
		sessionID, err := b.resolveSession(ch)
		if err != nil {
			reply(fmt.Sprintf("No active session: %v", err))
			return true
		}
		summary, err := b.pool.CompactSession(b.ctx, sessionID)
		if err != nil {
			logger().Error("compact session failed", "session_id", sessionID, "error", err)
			reply(fmt.Sprintf("Compaction failed: %v", err))
			return true
		}
		logger().Info("session compacted", "session_id", sessionID, "summary_len", len(summary))
		reply("Session compacted.")
		return true

	case "/model":
		args := strings.TrimSpace(strings.TrimPrefix(text, cmd))
		b.handleModelCommand(args, ch, reply)
		return true
	}

	return false
}

// shouldRespondInGroup checks whether the bot should respond based on group_mode.
// For QQ, group AT messages already imply the bot was mentioned, so "mention" mode
// always responds (the event itself is an @mention).
func (b *Bot) shouldRespondInGroup() bool {
	switch b.cfg.GroupMode {
	case "disabled":
		return false
	default:
		return true
	}
}

const welcomeMessage = "Hi! I'm Anna -- your local AI assistant.\n\n" +
	"Commands:\n" +
	"/new -- Start a fresh session\n" +
	"/compact -- Compress conversation history\n" +
	"/model -- Switch between models\n\n" +
	"Just send me a message to get started."

// sendReply is a convenience wrapper that dispatches to the correct scope.
func (b *Bot) sendReply(targetID, msgID, text string, scope messageScope) {
	switch scope {
	case scopeC2C:
		b.replyC2C(b.ctx, targetID, msgID, text)
	case scopeGroup:
		b.replyGroup(b.ctx, targetID, msgID, text)
	}
}

// replyC2C sends a text reply to a C2C (private) conversation.
func (b *Bot) replyC2C(ctx context.Context, userID, msgID, text string) {
	msg := dto.MessageToCreate{
		Content: text,
		MsgType: dto.TextMsg,
		MsgID:   msgID,
	}
	if _, err := b.api.PostC2CMessage(ctx, userID, msg); err != nil {
		logger().Error("c2c reply failed", "user_id", userID, "error", err)
	}
}

// replyGroup sends a text reply to a group conversation.
func (b *Bot) replyGroup(ctx context.Context, groupID, msgID, text string) {
	msg := dto.MessageToCreate{
		Content: text,
		MsgType: dto.TextMsg,
		MsgID:   msgID,
	}
	if _, err := b.api.PostGroupMessage(ctx, groupID, msg); err != nil {
		logger().Error("group reply failed", "group_id", groupID, "error", err)
	}
}
