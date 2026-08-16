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

// sendCardReplyInThread sends a card reply in the correct thread context.
// When rootID is non-empty, the card is sent as a reply to the root message.
func (b *Bot) sendCardReplyInThread(ctx context.Context, rootID, replyMsgID, text string) (string, error) {
	return b.sendCardReply(ctx, threadReplyTarget(replyMsgID, rootID), text, rootID != "")
}

// sendFinalResponseInThread delivers the completed response with thread awareness.
// It returns an egress error to the group dispatcher so its existing outbox
// retry path preserves at-least-once delivery. Only the final dispatcher
// attempt reports a terminal failure; earlier attempts remain invisible while
// the outbox retries them.
func (b *Bot) sendFinalResponseInThread(ctx context.Context, chatID, replyMsgID, rootID, sentMsgID, response string, refs []renderrefs.Reference, isGroup, reportFailure bool) error {
	replyTo := threadReplyTarget(replyMsgID, rootID)
	response = appendReferenceSection(response, refs, isGroup)

	if _, err := buildCardContent(response); err != nil {
		logger().Error("final card build failed; falling back to plain text", "error", err, "chat_id", chatID, "root_id", rootID)
		return b.sendPlainTextReply(ctx, replyTo, rootID, response)
	}

	if sentMsgID != "" {
		chunks := channel.SplitMessage(response, feishuMaxMessageLen)
		if err := b.patchMessage(ctx, sentMsgID, chunks[0]); err != nil {
			if errors.Is(err, errCardContentBuild) {
				logger().Error("final card build failed; falling back to plain text", "error", err, "chat_id", chatID, "root_id", rootID)
				return b.sendPlainTextReply(ctx, replyTo, rootID, response)
			}
			if reportFailure {
				b.reportDeliveryFailure(ctx, chatID, rootID, replyTo, sentMsgID, err)
			}
			return fmt.Errorf("patch final response: %w", err)
		}
		for _, chunk := range chunks[1:] {
			if _, err := b.sendCardReply(ctx, replyTo, chunk, rootID != ""); err != nil {
				if errors.Is(err, errCardContentBuild) {
					logger().Error("overflow card build failed; falling back to plain text", "error", err, "chat_id", chatID, "root_id", rootID)
					if fallbackErr := b.sendPlainTextReply(ctx, replyTo, rootID, chunk); fallbackErr != nil {
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

	chunks := channel.SplitMessage(response, feishuMaxMessageLen)
	for _, chunk := range chunks {
		if _, err := b.sendCardReply(ctx, replyTo, chunk, rootID != ""); err != nil {
			if errors.Is(err, errCardContentBuild) {
				logger().Error("final card build failed; falling back to plain text", "error", err, "chat_id", chatID, "root_id", rootID)
				if fallbackErr := b.sendPlainTextReply(ctx, replyTo, rootID, chunk); fallbackErr != nil {
					return fmt.Errorf("send final plain-text fallback: %w", fallbackErr)
				}
				continue
			}
			if reportFailure {
				b.reportDeliveryFailure(ctx, chatID, rootID, replyTo, "", err)
			}
			return fmt.Errorf("send final response: %w", err)
		}
	}
	return nil
}

func (b *Bot) reportDeliveryFailure(ctx context.Context, chatID, rootID, replyTo, sentMsgID string, deliveryErr error) {
	const terminalFailure = "⚠️ Response delivery failed. Please try again."
	var err error
	if sentMsgID != "" {
		err = b.patchMessage(ctx, sentMsgID, terminalFailure)
	} else {
		_, err = b.sendCardReply(ctx, replyTo, terminalFailure, rootID != "")
	}
	if err != nil {
		logger().Error("Feishu terminal delivery-failure notice also failed", "chat_id", chatID, "root_id", rootID, "reply_to", replyTo, "error", err)
	}
	logger().Error("Feishu response delivery failed", "chat_id", chatID, "root_id", rootID, "reply_to", replyTo, "error", deliveryErr)
}

// sendImageInThread sends an image in the correct thread context.
func (b *Bot) sendImageInThread(chatID, replyMsgID, rootID string, img channel.ImageEvent) {
	b.sendImage(chatID, threadReplyTarget(replyMsgID, rootID), img, rootID != "")
}

// sendFileInThread sends a file in the correct thread context.
func (b *Bot) sendFileInThread(chatID, replyMsgID, rootID string, file channel.FileEvent) {
	b.sendFile(chatID, threadReplyTarget(replyMsgID, rootID), file, rootID != "")
}
