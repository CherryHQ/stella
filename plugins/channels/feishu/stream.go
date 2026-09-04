package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

const streamEditInterval = 250 * time.Millisecond

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
func (b *Bot) streamResponseInThread(ctx context.Context, events <-chan channel.Event, chatID, replyMsgID, rootID, deliveryKey string) (string, string, []channel.ImageEvent, []channel.FileEvent, []renderrefs.Reference, time.Duration, error) {
	startTime := nowFunc()

	var sb strings.Builder
	var streamErr error
	timeline := &streamTimeline{}
	var sentMsgID string
	var images []channel.ImageEvent
	var files []channel.FileEvent
	var refs []renderrefs.Reference
	phase := phaseThinking
	deliveryUUID := stableDeliveryUUID(b.Name(), chatID, threadReplyTarget(replyMsgID, rootID), deliveryKey, "stream-card")

	// Phase 1: Send "Thinking..." card immediately.
	msgID, err := b.sendCardReplyInThreadWithOptions(ctx, rootID, replyMsgID, thinkingContent(), cardStatusRunning, deliveryUUID)
	switch {
	case err != nil:
		logger().Warn("thinking card failed", "chat_id", chatID, "root_id", rootID, "error", err)
	case msgID == "":
	default:
		sentMsgID = msgID
	}

	ticker := time.NewTicker(streamEditInterval)
	defer ticker.Stop()
	patchDone := make(chan error, 1)
	dirty := false
	patchInFlight := false

	startPatch := func() {
		current := sb.String()
		currentTimeline := timeline.latestMarkdown()
		if patchInFlight || sentMsgID == "" || (strings.TrimSpace(current) == "" && currentTimeline == "") {
			return
		}
		display := buildStreamDisplay(current, currentTimeline)
		dirty = false
		patchInFlight = true
		go func() {
			patchDone <- b.patchMessageForStatus(ctx, sentMsgID, display, cardStatusRunning)
		}()
	}

	eventsOpen := true
	for eventsOpen {
		select {
		case <-ctx.Done():
			streamErr = ctx.Err()
			eventsOpen = false
		case err := <-patchDone:
			patchInFlight = false
			if err != nil {
				logger().Warn("stream update failed", "error", err)
			}
		case <-ticker.C:
			if dirty {
				startPatch()
			}
		case evt, ok := <-events:
			if !ok {
				eventsOpen = false
				continue
			}
			if evt.Err != nil {
				streamErr = evt.Err
				eventsOpen = false
				continue
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
				timeline.handleTool(evt.ToolUse)
			}
			timeline.addReasoning(evt.Reasoning)

			sb.WriteString(evt.Text)

			// Transition to generating phase once we have content.
			if phase == phaseThinking && (strings.TrimSpace(sb.String()) != "" || timeline.latestMarkdown() != "") {
				phase = phaseGenerating
			}

			dirty = true
			if evt.ToolUse != nil && !patchInFlight {
				startPatch()
			}
		}
	}

	// Drain the latest running snapshot before the terminal render. If the
	// initial request had an unknown outcome, retry with the same UUID so Feishu
	// can return the already-created message instead of creating a duplicate.
	if patchInFlight {
		select {
		case err := <-patchDone:
			if err != nil {
				logger().Warn("stream update failed", "error", err)
			}
		case <-ctx.Done():
			streamErr = ctx.Err()
		}
	}
	if dirty && streamErr == nil {
		display := buildStreamDisplay(sb.String(), timeline.latestMarkdown())
		if sentMsgID == "" {
			msgID, err := b.sendCardReplyInThreadWithOptions(ctx, rootID, replyMsgID, display, cardStatusRunning, deliveryUUID)
			if err != nil {
				logger().Warn("stream reply failed", "error", err)
			} else {
				sentMsgID = msgID
			}
		} else if err := b.patchMessageForStatus(ctx, sentMsgID, display, cardStatusRunning); err != nil {
			logger().Warn("stream update failed", "error", err)
		}
	}

	// Phase 3: Complete -- remove cursor (final patch is handled by handleMessage
	// via sendFinalResponseInThread with elapsed time appended).
	elapsed := nowFunc().Sub(startTime)

	response := sb.String()
	if renderedTimeline := timeline.markdown(false); renderedTimeline != "" {
		response += "\n\n" + renderedTimeline
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
	return b.sendBuiltCardReply(ctx, replyMsgID, content, replyInThread, "")
}

func (b *Bot) sendCardReplyWithOptions(ctx context.Context, replyMsgID, text string, replyInThread bool, status cardStatus, uuid string) (string, error) {
	content, err := buildCardContentForStatus(text, status)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errCardContentBuild, err)
	}
	return b.sendBuiltCardReply(ctx, replyMsgID, content, replyInThread, uuid)
}

func (b *Bot) sendBuiltCardReply(ctx context.Context, replyMsgID, content string, replyInThread bool, uuid string) (string, error) {
	var messageID string
	err := b.retryFeishuSend(ctx, "reply card", func(ctx context.Context) error {
		var sendErr error
		messageID, sendErr = b.replyCardWithUUID(ctx, replyMsgID, content, replyInThread, uuid)
		return sendErr
	})
	return messageID, err
}

// sendCardToChatWithOptions posts a card into the chat itself, with nothing to
// reply to. Peer-post and stall-nudge group turns have no platform message.
func (b *Bot) sendCardToChatWithOptions(ctx context.Context, chatID, text string, status cardStatus, uuid string) (string, error) {
	content, err := buildCardContentForStatus(text, status)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errCardContentBuild, err)
	}
	return b.sendBuiltCardToChat(ctx, chatID, content, uuid)
}

func (b *Bot) sendBuiltCardToChat(ctx context.Context, chatID, content, uuid string) (string, error) {
	var messageID string
	err := b.retryFeishuSend(ctx, "send card", func(ctx context.Context) error {
		if b.createMessageFn != nil {
			var sendErr error
			messageID, sendErr = b.createMessageFn(ctx, chatID, larkim.MsgTypeInteractive, content)
			return sendErr
		}
		if b.client == nil {
			return fmt.Errorf("feishu client is not initialized")
		}
		body := larkim.NewCreateMessageReqBodyBuilder().
			MsgType(larkim.MsgTypeInteractive).
			ReceiveId(chatID).
			Content(content)
		if uuid != "" {
			body.Uuid(uuid)
		}
		resp, sendErr := b.client.Im.Message.Create(ctx,
			larkim.NewCreateMessageReqBuilder().
				ReceiveIdType(receiveIDTypeForChatID(chatID)).
				Body(body.Build()).
				Build())
		if sendErr != nil {
			return sendErr
		}
		if !resp.Success() {
			return newFeishuAPIError(resp.Code, resp.Msg, resp.Header)
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
			return newFeishuAPIError(resp.Code, resp.Msg, resp.Header)
		}
		return nil
	})
}

func (b *Bot) replyCardWithUUID(ctx context.Context, replyMsgID, content string, replyInThread bool, uuid string) (string, error) {
	if b.replyCardFn != nil {
		return b.replyCardFn(ctx, replyMsgID, content)
	}
	if b.client == nil {
		return "", fmt.Errorf("feishu client is not initialized")
	}
	resp, err := b.client.Im.Message.Reply(ctx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(replyMsgID).
			Body(replyMessageBodyWithUUID(larkim.MsgTypeInteractive, content, replyInThread, uuid)).
			Build())
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", newFeishuAPIError(resp.Code, resp.Msg, resp.Header)
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
	return replyMessageBodyWithUUID(msgType, content, replyInThread, "")
}

func replyMessageBodyWithUUID(msgType, content string, replyInThread bool, uuid string) *larkim.ReplyMessageReqBody {
	body := larkim.NewReplyMessageReqBodyBuilder().MsgType(msgType).Content(content)
	if replyInThread {
		body.ReplyInThread(true)
	}
	if uuid != "" {
		body.Uuid(uuid)
	}
	return body.Build()
}

func (b *Bot) patchMessageForStatus(ctx context.Context, messageID, text string, status cardStatus) error {
	content, err := buildCardContentForStatus(text, status)
	if err != nil {
		return fmt.Errorf("%w: %w", errCardContentBuild, err)
	}
	return b.patchBuiltCard(ctx, messageID, content)
}

func (b *Bot) patchBuiltCard(ctx context.Context, messageID, content string) error {
	return b.retryFeishuSend(ctx, "patch card", func(ctx context.Context) error {
		return b.patchCard(ctx, messageID, content)
	})
}

func stableDeliveryUUID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return "stella_" + hex.EncodeToString(digest[:])[:43]
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
		return newFeishuAPIError(resp.Code, resp.Msg, resp.Header)
	}
	return nil
}

// buildStreamDisplay constructs the streaming display text with the latest
// timeline panel and cursor, truncated to Feishu's message length limit.
func buildStreamDisplay(text, timeline string) string {
	display := text
	suffix := typingCursor
	if timeline != "" {
		suffix = "\n\n" + timeline + "\n" + typingCursor
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
