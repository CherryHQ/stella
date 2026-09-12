package feishu

import (
	"context"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

var errCardContentBuild = errors.New("build Feishu card content")

// threadReplyTarget returns rootID if non-empty (thread reply), otherwise messageID.
func threadReplyTarget(messageID, rootID string) string {
	if rootID != "" {
		return rootID
	}
	return messageID
}

// replyInThread sends a text reply. When rootID is non-empty, the reply
// targets the root message so it appears in the thread.
func (b *Bot) replyInThread(ctx context.Context, messageID, rootID, text string) {
	if err := b.replyText(ctx, threadReplyTarget(messageID, rootID), text, rootID != ""); err != nil {
		logger().Error("reply in thread failed", "message_id", messageID, "root_id", rootID, "error", err)
	}
}

func (b *Bot) sendPlainTextReply(ctx context.Context, messageID, rootID, text string) error {
	return b.replyText(ctx, messageID, text, rootID != "")
}

func (b *Bot) sendCardReplyInThreadWithOptions(ctx context.Context, rootID, replyMsgID, text string, status cardStatus, uuid string) (string, error) {
	return b.sendCardReplyWithOptions(ctx, threadReplyTarget(replyMsgID, rootID), text, rootID != "", status, uuid)
}

// deliverCardWithOptions replies to replyTo, or posts into the chat when a
// group turn was woken without a platform message behind it.
func (b *Bot) deliverCardWithOptions(ctx context.Context, chatID, replyTo, text string, replyInThread bool, status cardStatus, uuid string) (string, error) {
	if replyTo == "" {
		return b.sendCardToChatWithOptions(ctx, chatID, text, status, uuid)
	}
	return b.sendCardReplyWithOptions(ctx, replyTo, text, replyInThread, status, uuid)
}

// deliverPlainText is deliverCard's fallback sibling for a failed card build.
func (b *Bot) deliverPlainText(ctx context.Context, chatID, replyTo, rootID, text string) error {
	if replyTo == "" {
		return b.sendTextToChat(ctx, chatID, text)
	}
	return b.sendPlainTextReply(ctx, replyTo, rootID, text)
}

// sendFinalResponseInThread delivers the completed response with thread awareness.
// It returns an egress error to the group dispatcher so its existing outbox
// retry path preserves at-least-once delivery. Only the final dispatcher
// attempt reports a terminal failure; earlier attempts remain invisible while
// the outbox retries them.
func (b *Bot) sendFinalResponseInThread(ctx context.Context, chatID, replyMsgID, rootID, sentMsgID, response string, refs []renderrefs.Reference, isGroup, reportFailure bool) error {
	return b.sendFinalResponseInThreadWithOptions(ctx, chatID, replyMsgID, rootID, sentMsgID, response, refs, isGroup, reportFailure, cardStatusCompleted, "", nil)
}

// check — when non-nil — re-validates channel ownership before every platform
// call inside this multi-call helper: the durable op path passes the op's
// lease guard so a fenced-out owner stops before the next SDK call.
func (b *Bot) sendFinalResponseInThreadWithOptions(ctx context.Context, chatID, replyMsgID, rootID, sentMsgID, response string, refs []renderrefs.Reference, isGroup, reportFailure bool, status cardStatus, deliveryKey string, check func(context.Context) error) error {
	replyTo := threadReplyTarget(replyMsgID, rootID)
	response = appendReferenceSection(response, refs, isGroup)
	chunks := splitCardText(response, status)
	streamUUID := stableDeliveryUUID(b.Name(), chatID, replyTo, deliveryKey, "stream-card")
	guard := func() error {
		if check != nil {
			return check(ctx)
		}
		return nil
	}

	if sentMsgID != "" {
		if err := b.patchMessageForStatus(ctx, sentMsgID, chunks[0], status); err != nil {
			if errors.Is(err, errCardContentBuild) {
				logger().Error("final card build failed; falling back to plain text", "error", err, "chat_id", chatID, "root_id", rootID)
				if gerr := guard(); gerr != nil {
					return gerr
				}
				return b.deliverPlainText(ctx, chatID, replyTo, rootID, response)
			}
			if reportFailure {
				b.reportDeliveryFailure(ctx, chatID, rootID, replyTo, sentMsgID, err)
			}
			return fmt.Errorf("patch final response: %w", err)
		}
		for i, chunk := range chunks[1:] {
			chunkUUID := stableDeliveryUUID(streamUUID, fmt.Sprintf("overflow-%d", i+1))
			if err := guard(); err != nil {
				return err
			}
			if _, err := b.deliverCardWithOptions(ctx, chatID, replyTo, chunk, rootID != "", status, chunkUUID); err != nil {
				if errors.Is(err, errCardContentBuild) {
					logger().Error("overflow card build failed; falling back to plain text", "error", err, "chat_id", chatID, "root_id", rootID)
					if gerr := guard(); gerr != nil {
						return gerr
					}
					if fallbackErr := b.deliverPlainText(ctx, chatID, replyTo, rootID, chunk); fallbackErr != nil {
						return fmt.Errorf("send overflow plain-text fallback: %w", fallbackErr)
					}
					continue
				}
				if reportFailure {
					// The initial card already contains the first chunk. Append the
					// failure notice instead of overwriting delivered content.
					b.reportDeliveryFailure(ctx, chatID, rootID, replyTo, "", err)
				}
				return fmt.Errorf("send overflow response: %w", err)
			}
		}
		return nil
	}

	for i, chunk := range chunks {
		chunkUUID := stableDeliveryUUID(streamUUID, fmt.Sprintf("chunk-%d", i))
		if i == 0 {
			chunkUUID = streamUUID
		}
		if err := guard(); err != nil {
			return err
		}
		messageID, err := b.deliverCardWithOptions(ctx, chatID, replyTo, chunk, rootID != "", status, chunkUUID)
		if err != nil {
			if errors.Is(err, errCardContentBuild) {
				logger().Error("final card build failed; falling back to plain text", "error", err, "chat_id", chatID, "root_id", rootID)
				if gerr := guard(); gerr != nil {
					return gerr
				}
				if fallbackErr := b.deliverPlainText(ctx, chatID, replyTo, rootID, chunk); fallbackErr != nil {
					return fmt.Errorf("send final plain-text fallback: %w", fallbackErr)
				}
				continue
			}
			if reportFailure {
				b.reportDeliveryFailure(ctx, chatID, rootID, replyTo, "", err)
			}
			return fmt.Errorf("send final response: %w", err)
		}
		if i == 0 && messageID != "" && deliveryKey != "" {
			// A retry may have recovered the ID of an earlier Thinking card with
			// the same UUID. Patch once so its content is terminal either way.
			if err := guard(); err != nil {
				return err
			}
			if err := b.patchMessageForStatus(ctx, messageID, chunk, status); err != nil {
				return fmt.Errorf("finalize recovered response: %w", err)
			}
		}
	}
	return nil
}

func (b *Bot) reportDeliveryFailure(ctx context.Context, chatID, rootID, replyTo, sentMsgID string, deliveryErr error) {
	const terminalFailure = "⚠️ Response delivery failed. Please try again."
	var err error
	if sentMsgID != "" {
		err = b.patchMessageForStatus(ctx, sentMsgID, terminalFailure, cardStatusFailed)
	} else {
		_, err = b.sendCardReply(ctx, replyTo, terminalFailure, rootID != "")
	}
	if err != nil {
		logger().Error("Feishu terminal delivery-failure notice also failed", "chat_id", chatID, "root_id", rootID, "reply_to", replyTo, "error", err)
	}
	logger().Error("Feishu response delivery failed", "chat_id", chatID, "root_id", rootID, "reply_to", replyTo, "error", deliveryErr)
}

// sendImageInThread sends an image in the correct thread context. check —
// when non-nil — re-validates channel ownership between the upload and the
// message create so a fenced-out owner stops mid-operation.
func (b *Bot) sendImageInThread(chatID, replyMsgID, rootID string, img channel.ImageEvent, check func() error) error {
	return b.sendImage(chatID, threadReplyTarget(replyMsgID, rootID), img, rootID != "", check)
}

// sendFileInThread sends a file in the correct thread context.
func (b *Bot) sendFileInThread(chatID, replyMsgID, rootID string, file channel.FileEvent, check func() error) error {
	return b.sendFile(chatID, threadReplyTarget(replyMsgID, rootID), file, rootID != "", check)
}
