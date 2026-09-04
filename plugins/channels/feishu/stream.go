package feishu

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/renderrefs"
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
func (b *Bot) streamResponseInThread(ctx context.Context, events <-chan channel.Event, chatID, replyMsgID, rootID string) (string, string, []channel.ImageEvent, []channel.FileEvent, []renderrefs.Reference, time.Duration, error) {
	startTime := nowFunc()

	var sb strings.Builder
	var streamErr error
	var currentTool string
	tools := &channel.ToolTracker{}
	var sentMsgID string
	var images []channel.ImageEvent
	var files []channel.FileEvent
	var refs []renderrefs.Reference
	phase := phaseThinking
	lastSend := time.Time{}

	// Phase 1: Send "Thinking..." card immediately.
	msgID, err := b.sendCardReplyInThread(ctx, rootID, replyMsgID, thinkingContent())
	switch {
	case err != nil:
		logger().Warn("thinking card failed", "chat_id", chatID, "root_id", rootID, "error", err)
	case msgID == "":
	default:
		sentMsgID = msgID
	}

	for evt := range events {
		if evt.Err != nil {
			streamErr = evt.Err
			break
		}

		if len(evt.References) > 0 {
			refs = append(refs, evt.References...)
		}

		if evt.Image != nil {
			images = append(images, *evt.Image)
			continue
		}

		if evt.File != nil {
			files = append(files, *evt.File)
			continue
		}

		if evt.ToolUse != nil {
			tools.Handle(evt.ToolUse)
			line := channel.ToolLine(evt.ToolUse)
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
			msgID, err := b.sendCardReplyInThread(ctx, rootID, replyMsgID, display)
			if err != nil {
				logger().Warn("stream reply failed", "error", err)
			} else {
				sentMsgID = msgID
			}
		} else {
			if err := b.patchMessage(ctx, sentMsgID, display); err != nil {
				logger().Warn("stream update failed", "error", err)
			}
		}
		lastSend = now
	}

	// Phase 3: Complete -- remove cursor (final patch is handled by handleMessage
	// via sendFinalResponseInThread with elapsed time appended).
	elapsed := nowFunc().Sub(startTime)

	response := sb.String()
	if tools.HasHistory() {
		response += tools.RenderFinal()
	}
	return sentMsgID, response, images, files, dedupeReferences(refs), elapsed, streamErr
}

// sendCardReply sends an interactive card reply and returns the new message ID.
// Cards support the Patch API for in-place streaming edits.
func (b *Bot) sendCardReply(ctx context.Context, replyMsgID, text string, replyInThread bool) (string, error) {
	content, err := buildCardContent(text)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errCardContentBuild, err)
	}
	var messageID string
	err = b.retryFeishuSend(ctx, "reply card", func(ctx context.Context) error {
		var sendErr error
		messageID, sendErr = b.replyCard(ctx, replyMsgID, content, replyInThread)
		return sendErr
	})
	return messageID, err
}

// sendCardToChat posts a card into the chat itself, with nothing to reply to.
// A group turn woken by a peer's post or by a stall nudge has no platform
// message behind it, and the Reply API rejects an empty message_id -- so
// without this path those replies are lost rather than merely unthreaded.
func (b *Bot) sendCardToChat(ctx context.Context, chatID, text string) (string, error) {
	content, err := buildCardContent(text)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errCardContentBuild, err)
	}
	var messageID string
	err = b.retryFeishuSend(ctx, "send card", func(ctx context.Context) error {
		if b.createMessageFn != nil {
			var sendErr error
			messageID, sendErr = b.createMessageFn(ctx, chatID, larkim.MsgTypeInteractive, content)
			return sendErr
		}
		if b.client == nil {
			return fmt.Errorf("feishu client is not initialized")
		}
		resp, sendErr := b.client.Im.Message.Create(ctx,
			larkim.NewCreateMessageReqBuilder().
				ReceiveIdType(receiveIDTypeForChatID(chatID)).
				Body(larkim.NewCreateMessageReqBodyBuilder().
					MsgType(larkim.MsgTypeInteractive).
					ReceiveId(chatID).
					Content(content).
					Build()).
				Build())
		if sendErr != nil {
			return sendErr
		}
		if !resp.Success() {
			return &feishuAPIError{code: resp.Code, msg: resp.Msg}
		}
		if resp.Data != nil && resp.Data.MessageId != nil {
			messageID = *resp.Data.MessageId
		}
		return nil
	})
	return messageID, err
}

// sendTextToChat is sendCardToChat's plain-text sibling, used when card
// rendering itself failed.
func (b *Bot) sendTextToChat(ctx context.Context, chatID, text string) error {
	return b.retryFeishuSend(ctx, "send text", func(ctx context.Context) error {
		if b.createMessageFn != nil {
			_, sendErr := b.createMessageFn(ctx, chatID, larkim.MsgTypeText, textContent(text))
			return sendErr
		}
		if b.client == nil {
			return fmt.Errorf("feishu client is not initialized")
		}
		resp, sendErr := b.client.Im.Message.Create(ctx,
			larkim.NewCreateMessageReqBuilder().
				ReceiveIdType(receiveIDTypeForChatID(chatID)).
				Body(larkim.NewCreateMessageReqBodyBuilder().
					MsgType(larkim.MsgTypeText).
					ReceiveId(chatID).
					Content(textContent(text)).
					Build()).
				Build())
		if sendErr != nil {
			return sendErr
		}
		if !resp.Success() {
			return &feishuAPIError{code: resp.Code, msg: resp.Msg}
		}
		return nil
	})
}

func (b *Bot) replyCard(ctx context.Context, replyMsgID, content string, replyInThread bool) (string, error) {
	if b.replyCardFn != nil {
		return b.replyCardFn(ctx, replyMsgID, content)
	}
	if b.client == nil {
		return "", fmt.Errorf("feishu client is not initialized")
	}
	resp, err := b.client.Im.Message.Reply(ctx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(replyMsgID).
			Body(replyMessageBody(larkim.MsgTypeInteractive, content, replyInThread)).
			Build())
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", &feishuAPIError{code: resp.Code, msg: resp.Msg}
	}
	if resp.Data != nil && resp.Data.MessageId != nil {
		return *resp.Data.MessageId, nil
	}
	return "", nil
}

// patchMessage edits an existing card message in place using the Patch API.
// replyMessageBody keeps every Reply API path consistent: replying to a
// thread root is not enough on its own; Feishu requires reply_in_thread.
func replyMessageBody(msgType, content string, replyInThread bool) *larkim.ReplyMessageReqBody {
	body := larkim.NewReplyMessageReqBodyBuilder().MsgType(msgType).Content(content)
	if replyInThread {
		body.ReplyInThread(true)
	}
	return body.Build()
}

func (b *Bot) patchMessage(ctx context.Context, messageID, text string) error {
	content, err := buildCardContent(text)
	if err != nil {
		return fmt.Errorf("%w: %w", errCardContentBuild, err)
	}
	return b.retryFeishuSend(ctx, "patch card", func(ctx context.Context) error {
		return b.patchCard(ctx, messageID, content)
	})
}

func (b *Bot) patchCard(ctx context.Context, messageID, content string) error {
	if b.patchCardFn != nil {
		return b.patchCardFn(ctx, messageID, content)
	}
	if b.client == nil {
		return fmt.Errorf("feishu client is not initialized")
	}
	resp, err := b.client.Im.Message.Patch(ctx,
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
		return &feishuAPIError{code: resp.Code, msg: resp.Msg}
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
		cutAt := max(feishuMaxMessageLen-len(suffix)-3, 0)
		for cutAt > 0 && !utf8.RuneStart(display[cutAt]) {
			cutAt--
		}
		display = display[:cutAt] + "..."
	}

	return display + suffix
}
