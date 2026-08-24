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

// deliverCard replies to replyTo, or posts into the chat when there is nothing
// to reply to. Every group turn that a peer post or a stall nudge woke arrives
// here with an empty replyTo.
func (b *Bot) deliverCard(ctx context.Context, chatID, replyTo, text string, replyInThread bool) (string, error) {
	if replyTo == "" {
		return b.sendCardToChat(ctx, chatID, text)
	}
	return b.sendCardReply(ctx, replyTo, text, replyInThread)
}

// deliverPlainText is deliverCard's fallback sibling for a failed card build.
func (b *Bot) deliverPlainText(ctx context.Context, chatID, replyTo, rootID, text string) error {
	if replyTo == "" {
		return b.sendTextToChat(ctx, chatID, text)
	}
	return b.sendPlainTextReply(ctx, replyTo, rootID, text)
}

// sendFinalResponseInThread delivers the completed response with thread awareness.
// It returns an egress error to the group dispatcher, which terminalizes an
// outcome-unknown publish without retrying it.
func (b *Bot) sendFinalResponseInThread(ctx context.Context, chatID, replyMsgID, rootID, sentMsgID, response string, refs []renderrefs.Reference, isGroup, reportFailure bool, streams ...*channel.ChatStream) error {
	check := func() error { return nil }
	if len(streams) > 0 {
		check = func() error { return streams[0].CheckOperation(ctx) }
	}
	replyTo := threadReplyTarget(replyMsgID, rootID)
	response = appendReferenceSection(response, refs, isGroup)

	if _, err := buildCardContent(response); err != nil {
		logger().Error("final card build failed; falling back to plain text", "error", err, "chat_id", chatID, "root_id", rootID)
		if err := check(); err != nil {
			return err
		}
		return b.deliverPlainText(ctx, chatID, replyTo, rootID, response)
	}

	if sentMsgID != "" {
		chunks := channel.SplitMessage(response, feishuMaxMessageLen)
		if err := check(); err != nil {
			return err
		}
		if err := b.patchMessage(ctx, sentMsgID, chunks[0]); err != nil {
			if errors.Is(err, errCardContentBuild) {
				logger().Error("final card build failed; falling back to plain text", "error", err, "chat_id", chatID, "root_id", rootID)
				if err := check(); err != nil {
					return err
				}
				return b.deliverPlainText(ctx, chatID, replyTo, rootID, response)
			}
			return fmt.Errorf("patch final response: %w", err)
		}
		for _, chunk := range chunks[1:] {
			if err := check(); err != nil {
				return err
			}
			if _, err := b.deliverCard(ctx, chatID, replyTo, chunk, rootID != ""); err != nil {
				if errors.Is(err, errCardContentBuild) {
					logger().Error("overflow card build failed; falling back to plain text", "error", err, "chat_id", chatID, "root_id", rootID)
					if err := check(); err != nil {
						return err
					}
					if fallbackErr := b.deliverPlainText(ctx, chatID, replyTo, rootID, chunk); fallbackErr != nil {
						return fmt.Errorf("send overflow plain-text fallback: %w", fallbackErr)
					}
					continue
				}
				return fmt.Errorf("send overflow response: %w", err)
			}
		}
		return nil
	}

	chunks := channel.SplitMessage(response, feishuMaxMessageLen)
	for _, chunk := range chunks {
		if err := check(); err != nil {
			return err
		}
		if _, err := b.deliverCard(ctx, chatID, replyTo, chunk, rootID != ""); err != nil {
			if errors.Is(err, errCardContentBuild) {
				logger().Error("final card build failed; falling back to plain text", "error", err, "chat_id", chatID, "root_id", rootID)
				if err := check(); err != nil {
					return err
				}
				if fallbackErr := b.deliverPlainText(ctx, chatID, replyTo, rootID, chunk); fallbackErr != nil {
					return fmt.Errorf("send final plain-text fallback: %w", fallbackErr)
				}
				continue
			}
			return fmt.Errorf("send final response: %w", err)
		}
	}
	return nil
}

// sendImageInThread sends an image in the correct thread context.
func (b *Bot) sendImageInThread(ctx context.Context, chatID, replyMsgID, rootID string, img channel.ImageEvent, stream *channel.ChatStream) error {
	return b.sendImage(ctx, chatID, threadReplyTarget(replyMsgID, rootID), img, rootID != "", stream)
}

// sendFileInThread sends a file in the correct thread context.
func (b *Bot) sendFileInThread(ctx context.Context, chatID, replyMsgID, rootID string, file channel.FileEvent, stream *channel.ChatStream) error {
	return b.sendFile(ctx, chatID, threadReplyTarget(replyMsgID, rootID), file, rootID != "", stream)
}
