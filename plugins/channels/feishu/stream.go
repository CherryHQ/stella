package feishu

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/pkg/channel"
)

const streamEditInterval = time.Second

const typingCursor = " \u258D"

// streamPhase tracks the current streaming phase.
type streamPhase int

const (
	phaseThinking   streamPhase = iota // Initial phase before text arrives
	phaseGenerating                    // Text is streaming
	phaseComplete                      // Stream finished
)

var toolEmoji = map[string]string{
	"bash":    "⚡",
	"read":    "📖",
	"write":   "✏️",
	"edit":    "🔧",
	"search":  "🔍",
	"default": "🔧",
}

// toolLine returns a short status line for a tool-use event.
func toolLine(t *channel.ToolUseEvent) string {
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

// thinkingContent returns the card content for the thinking phase.
func thinkingContent() string {
	return "⏳ Thinking..."
}

// elapsedFooter returns the elapsed time footer line.
func elapsedFooter(d time.Duration) string {
	return fmt.Sprintf("\n\n_Response time: %.1fs_", d.Seconds())
}

// nowFunc is a package-level variable for testability.
var nowFunc = time.Now

// streamResponseInThread consumes the agent event stream and progressively updates
// a reply message. Thread-aware: when rootID is non-empty, the initial card reply
// targets the thread root.
//
// Phases:
//  1. Thinking: sends initial card with "Thinking..." immediately
//  2. Generating: updates card with streaming content + cursor
//  3. Complete: final content with elapsed time footer
func (b *Bot) streamResponseInThread(events <-chan channel.Event, chatID, replyMsgID, rootID string) (string, string, []channel.ImageEvent, time.Duration, error) {
	startTime := nowFunc()

	var sb strings.Builder
	var streamErr error
	var currentTool string
	var sentMsgID string
	var images []channel.ImageEvent
	phase := phaseThinking
	lastSend := time.Time{}

	// Phase 1: Send "Thinking..." card immediately.
	msgID, err := b.sendCardReplyInThread(rootID, replyMsgID, thinkingContent())
	if err != nil {
		logger().Warn("thinking card failed", "error", err)
	} else {
		sentMsgID = msgID
	}

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

		// Transition to generating phase once we have content.
		if phase == phaseThinking && (strings.TrimSpace(sb.String()) != "" || currentTool != "") {
			phase = phaseGenerating
		}

		now := nowFunc()
		if now.Sub(lastSend) < streamEditInterval {
			continue
		}

		current := sb.String()
		if strings.TrimSpace(current) == "" && currentTool == "" {
			continue
		}

		// Phase 2: Generating -- content with cursor.
		display := buildStreamDisplay(current, currentTool)

		if sentMsgID == "" {
			// Fallback: if thinking card failed, send now.
			msgID, err := b.sendCardReplyInThread(rootID, replyMsgID, display)
			if err != nil {
				logger().Warn("stream reply failed", "error", err)
			} else {
				sentMsgID = msgID
			}
		} else {
			if err := b.patchMessage(sentMsgID, display); err != nil {
				logger().Warn("stream update failed", "error", err)
			}
		}
		lastSend = now
	}

	// Phase 3: Complete -- remove cursor (final patch is handled by handleMessage
	// via sendFinalResponseInThread with elapsed time appended).
	elapsed := nowFunc().Sub(startTime)

	return sentMsgID, sb.String(), images, elapsed, streamErr
}

// sendCardReply sends an interactive card reply and returns the new message ID.
// Cards support the Patch API for in-place streaming edits.
func (b *Bot) sendCardReply(replyMsgID, text string) (string, error) {
	content := cardContent(text)
	resp, err := b.client.Im.Message.Reply(b.ctx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(replyMsgID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeInteractive).
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

// patchMessage edits an existing card message in place using the Patch API.
func (b *Bot) patchMessage(messageID, text string) error {
	content := cardContent(text)
	resp, err := b.client.Im.Message.Patch(b.ctx,
		larkim.NewPatchMessageReqBuilder().
			MessageId(messageID).
			Body(larkim.NewPatchMessageReqBodyBuilder().
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
