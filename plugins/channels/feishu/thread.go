package feishu

import (
	"context"
	"errors"

	"github.com/CherryHQ/stella/internal/renderrefs"
	"github.com/CherryHQ/stella/pkg/channel"
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
	b.replyText(ctx, threadReplyTarget(messageID, rootID), text)
}

func (b *Bot) sendPlainTextReply(messageID, text string) {
	apiCtx, cancel := b.apiContext()
	defer cancel()
	b.replyText(apiCtx, messageID, text)
}

// sendCardReplyInThread sends a card reply in the correct thread context.
// When rootID is non-empty, the card is sent as a reply to the root message.
func (b *Bot) sendCardReplyInThread(rootID, replyMsgID, text string) (string, error) {
	return b.sendCardReply(threadReplyTarget(replyMsgID, rootID), text)
}

// sendFinalResponseInThread delivers the completed response with thread awareness.
func (b *Bot) sendFinalResponseInThread(chatID, replyMsgID, rootID, sentMsgID, response string, refs []renderrefs.Reference, isGroup bool) {
	replyTo := threadReplyTarget(replyMsgID, rootID)
	response = appendReferenceSection(response, refs, isGroup)

	if _, err := buildCardContent(response); err != nil {
		logger().Error("final card build failed", "error", err, "chat_id", chatID)
		b.sendPlainTextReply(replyTo, response)
		return
	}

	if sentMsgID != "" {
		chunks := channel.SplitMessage(response, feishuMaxMessageLen)
		if err := b.patchMessage(sentMsgID, chunks[0]); err != nil {
			if errors.Is(err, errCardContentBuild) {
				logger().Error("final card build failed", "error", err, "chat_id", chatID)
				b.sendPlainTextReply(replyTo, response)
				return
			}
			logger().Error("final update failed", "error", err, "chat_id", chatID)
		}
		for _, chunk := range chunks[1:] {
			if _, err := b.sendCardReply(replyTo, chunk); err != nil {
				if errors.Is(err, errCardContentBuild) {
					logger().Error("overflow card build failed", "error", err, "chat_id", chatID)
					b.sendPlainTextReply(replyTo, chunk)
					continue
				}
				logger().Error("send overflow chunk failed", "error", err, "chat_id", chatID)
			}
		}
		return
	}

	chunks := channel.SplitMessage(response, feishuMaxMessageLen)
	for _, chunk := range chunks {
		if _, err := b.sendCardReply(replyTo, chunk); err != nil {
			if errors.Is(err, errCardContentBuild) {
				logger().Error("final card build failed", "error", err, "chat_id", chatID)
				b.sendPlainTextReply(replyTo, chunk)
				continue
			}
			logger().Error("send final response failed", "error", err, "chat_id", chatID)
		}
	}
}

// sendImageInThread sends an image in the correct thread context.
func (b *Bot) sendImageInThread(chatID, replyMsgID, rootID string, img channel.ImageEvent) {
	b.sendImage(chatID, threadReplyTarget(replyMsgID, rootID), img)
}

// sendFileInThread sends a file in the correct thread context.
func (b *Bot) sendFileInThread(chatID, replyMsgID, rootID string, file channel.FileEvent) {
	b.sendFile(chatID, threadReplyTarget(replyMsgID, rootID), file)
}
