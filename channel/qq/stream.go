package qq

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tencent-connect/botgo/dto"
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
		if len(input) > 60 {
			input = input[:57] + "..."
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

// streamResponse consumes the agent event stream and progressively sends
// updates using QQ's native Stream API.
func (b *Bot) streamResponse(targetID, msgID, sessionID, prompt string, scope messageScope) (string, error) {
	events := b.pool.Chat(b.ctx, sessionID, prompt)

	var sb strings.Builder
	var streamErr error
	var currentTool string
	var streamMsgID string
	var seq uint32 = 1
	lastSend := time.Time{}

	for evt := range events {
		if evt.Err != nil {
			streamErr = evt.Err
			break
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

		newMsgID, err := b.sendStreamChunk(targetID, msgID, display, streamMsgID, seq, false, scope)
		if err != nil {
			logger().Warn("stream chunk failed", "error", err, "seq", seq)
		} else {
			if streamMsgID == "" {
				streamMsgID = newMsgID
			}
			seq++
		}
		lastSend = now
	}

	// Send a terminal stream chunk (State=10) to finalize the stream,
	// so QQ clients exit the "generating" state.
	if streamMsgID != "" {
		final := sb.String()
		if strings.TrimSpace(final) == "" {
			final = "(empty response)"
		}
		if _, err := b.sendStreamChunk(targetID, msgID, final, streamMsgID, seq, true, scope); err != nil {
			logger().Warn("stream done chunk failed", "error", err)
		}
	}

	return sb.String(), streamErr
}

// sendStreamChunk sends a streaming message chunk using QQ's Stream API.
// Returns the message ID from the first chunk (used as stream ID for subsequent chunks).
func (b *Bot) sendStreamChunk(targetID, replyMsgID, text, streamID string, seq uint32, done bool, scope messageScope) (string, error) {
	state := int32(1) // generating
	if done {
		state = 10 // body done
	}

	msg := dto.MessageToCreate{
		Content: text,
		MsgType: dto.TextMsg,
		MsgID:   replyMsgID,
		MsgSeq:  seq,
		Stream: &dto.Stream{
			State: state,
			ID:    streamID,
			Index: int32(seq),
		},
	}

	var (
		result *dto.Message
		err    error
	)
	switch scope {
	case scopeC2C:
		result, err = b.api.PostC2CMessage(b.ctx, targetID, msg)
	case scopeGroup:
		result, err = b.api.PostGroupMessage(b.ctx, targetID, msg)
	}

	if err != nil {
		return "", err
	}
	if result != nil {
		return result.ID, nil
	}
	return "", nil
}

// buildStreamDisplay constructs the streaming display text with tool indicator
// and cursor, truncated to QQ's message length limit (UTF-8 safe).
func buildStreamDisplay(text, currentTool string) string {
	display := text
	suffix := typingCursor
	if currentTool != "" {
		suffix = "\n\n" + currentTool + typingCursor
	}

	if len(suffix) >= qqMaxMessageLen {
		suffix = typingCursor
	}

	if len(display)+len(suffix) > qqMaxMessageLen {
		cutAt := qqMaxMessageLen - len(suffix) - 3
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
