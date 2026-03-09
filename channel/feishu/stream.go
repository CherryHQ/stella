package feishu

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/agent/runner"
)

const streamEditInterval = time.Second

const typingCursor = " \u258D"

var toolEmoji = map[string]string{
	"bash":    "⚡",
	"read":    "📖",
	"write":   "✏️",
	"edit":    "🔧",
	"search":  "🔍",
	"default": "🔧",
}

// toolLine returns a short status line for a tool-use event.
func toolLine(t *runner.ToolUseEvent) string {
	emoji, ok := toolEmoji[t.Tool]
	if !ok {
		emoji = toolEmoji["default"]
	}
	switch t.Status {
	case "running":
		input := t.Input
		if utf8.RuneCountInString(input) > 60 {
			r := []rune(input)
			input = string(r[:57]) + "..."
		}
		if input != "" {
			return fmt.Sprintf("%s %s: %s", emoji, t.Tool, input)
		}
		return fmt.Sprintf("%s %s", emoji, t.Tool)
	case "error":
		return fmt.Sprintf("❌ %s failed", t.Tool)
	default:
		return ""
	}
}

// streamResponse consumes the agent event stream and progressively updates
// a reply message using Feishu's Message Update API. Returns the sent message ID,
// final text, collected images, and any stream error.
func (b *Bot) streamResponse(chatID, replyMsgID, sessionID string, content runner.MessageContent) (string, string, []runner.ImageEvent, error) {
	events := b.pool.Chat(b.ctx, sessionID, content)

	var sb strings.Builder
	var streamErr error
	var currentTool string
	var sentMsgID string
	var images []runner.ImageEvent
	lastSend := time.Time{}

	for evt := range events {
		if evt.Err != nil {
			streamErr = evt.Err
			break
		}

		if evt.Image != nil {
			images = append(images, *evt.Image)
			continue
		}

		if evt.ToolUse != nil {
			line := toolLine(evt.ToolUse)
			if line != "" {
				currentTool = line
			} else {
				currentTool = ""
			}
			lastSend = time.Time{}
		}

		sb.WriteString(evt.Text)

		now := time.Now()
		if now.Sub(lastSend) < streamEditInterval {
			continue
		}

		current := sb.String()
		if strings.TrimSpace(current) == "" && currentTool == "" {
			continue
		}

		display := buildStreamDisplay(current, currentTool)

		if sentMsgID == "" {
			// Send initial reply.
			msgID, err := b.sendReplyMessage(replyMsgID, display)
			if err != nil {
				logger().Warn("stream reply failed", "error", err)
			} else {
				sentMsgID = msgID
			}
		} else {
			// Update existing message.
			if err := b.updateMessage(sentMsgID, display); err != nil {
				logger().Warn("stream update failed", "error", err)
			}
		}
		lastSend = now
	}

	// Final update to remove cursor.
	if sentMsgID != "" {
		final := sb.String()
		if strings.TrimSpace(final) != "" {
			if err := b.updateMessage(sentMsgID, final); err != nil {
				logger().Warn("final update failed", "error", err)
			}
		}
	}

	return sentMsgID, sb.String(), images, streamErr
}

// sendReplyMessage sends a text reply and returns the new message ID.
func (b *Bot) sendReplyMessage(replyMsgID, text string) (string, error) {
	content := textContent(text)
	resp, err := b.client.Im.Message.Reply(b.ctx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(replyMsgID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeText).
				Content(content).
				Build()).
			Build())
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data != nil && resp.Data.MessageId != nil {
		return *resp.Data.MessageId, nil
	}
	return "", nil
}

// updateMessage edits an existing message in place.
func (b *Bot) updateMessage(messageID, text string) error {
	content := textContent(text)
	resp, err := b.client.Im.Message.Update(b.ctx,
		larkim.NewUpdateMessageReqBuilder().
			MessageId(messageID).
			Body(larkim.NewUpdateMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeText).
				Content(content).
				Build()).
			Build())
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// buildStreamDisplay constructs the streaming display text with tool indicator
// and cursor, truncated to Feishu's message length limit (UTF-8 safe).
func buildStreamDisplay(text, currentTool string) string {
	display := text
	suffix := typingCursor
	if currentTool != "" {
		suffix = "\n\n" + currentTool + typingCursor
	}

	if len(suffix) >= feishuMaxMessageLen {
		suffix = typingCursor
	}

	if len(display)+len(suffix) > feishuMaxMessageLen {
		cutAt := feishuMaxMessageLen - len(suffix) - 3
		if cutAt < 0 {
			cutAt = 0
		}
		for cutAt > 0 && !utf8.RuneStart(display[cutAt]) {
			cutAt--
		}
		display = display[:cutAt] + "..."
	}

	return display + suffix
}
