package feishu

import (
	"context"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/channel"
)

// threadChannelCtx builds a channelCtx string that includes thread context.
// When rootID is non-empty, the session is scoped to the thread; otherwise
// it falls back to the regular group or private context.
func threadChannelCtx(chatID, chatType, rootID string) (channelCtx string, isGroup bool) {
	isGroup = chatType == "group"
	if isGroup && chatID != "" {
		if rootID != "" {
			channelCtx = "group:" + chatID + ":thread:" + rootID
		} else {
			channelCtx = "group:" + chatID
		}
	} else {
		channelCtx = "private"
	}
	return channelCtx, isGroup
}

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

// sendCardReplyInThread sends a card reply in the correct thread context.
// When rootID is non-empty, the card is sent as a reply to the root message.
func (b *Bot) sendCardReplyInThread(rootID, replyMsgID, text string) (string, error) {
	return b.sendCardReply(threadReplyTarget(replyMsgID, rootID), text)
}

// sendFinalResponseInThread delivers the completed response with thread awareness.
func (b *Bot) sendFinalResponseInThread(chatID, replyMsgID, rootID, sentMsgID, response string) {
	replyTo := threadReplyTarget(replyMsgID, rootID)

	if sentMsgID != "" {
		chunks := splitMessage(response)
		if err := b.patchMessage(sentMsgID, chunks[0]); err != nil {
			logger().Error("final update failed", "error", err, "chat_id", chatID)
		}
		for _, chunk := range chunks[1:] {
			if _, err := b.sendCardReply(replyTo, chunk); err != nil {
				logger().Error("send overflow chunk failed", "error", err, "chat_id", chatID)
			}
		}
		return
	}

	chunks := splitMessage(response)
	for _, chunk := range chunks {
		if _, err := b.sendCardReply(replyTo, chunk); err != nil {
			logger().Error("send final response failed", "error", err, "chat_id", chatID)
		}
	}
}

// sendImageInThread sends an image in the correct thread context.
func (b *Bot) sendImageInThread(chatID, replyMsgID, rootID string, img runner.ImageEvent) {
	b.sendImage(chatID, threadReplyTarget(replyMsgID, rootID), img)
}

// resolveWithThread performs resolution with thread-aware channelCtx.
func (b *Bot) resolveWithThread(openID, chatID, chatType, rootID string) (*channel.ResolvedChat, error) {
	channelCtx, isGroup := threadChannelCtx(chatID, chatType, rootID)

	// Use the channelCtx-aware resolve path. The standard resolve() builds
	// its own channelCtx from isGroup+chatID, but for threads we need a
	// custom channelCtx. We call channel.Resolve directly with the group
	// flag and then override the session key.
	rc, err := channel.Resolve(
		context.Background(),
		b.poolManager,
		b.store,
		b.authStore,
		b.engine,
		channel.PlatformFeishu,
		openID,
		"",
		chatID,
		isGroup,
	)
	if err != nil {
		return nil, err
	}

	// Override session key when in a thread (channelCtx differs from
	// the default "group:chatID").
	if rootID != "" && isGroup {
		rc.SessionKey = replaceChannelCtx(rc.SessionKey, channelCtx)
	}

	return rc, nil
}

// replaceChannelCtx replaces the channelCtx suffix in a session key.
// Session key format: {prefix}:{channelCtx}
// For group sessions the channelCtx is "group:chatID"; we replace it
// with the thread-aware variant.
func replaceChannelCtx(sessionKey, newCtx string) string {
	// The channelCtx is the last colon-separated segment(s) that starts
	// with "group:" or "private". We find "group:" and replace from there.
	for i := 0; i < len(sessionKey); i++ {
		if i+6 <= len(sessionKey) && sessionKey[i:i+6] == "group:" {
			return sessionKey[:i] + newCtx
		}
		if i+7 <= len(sessionKey) && sessionKey[i:i+7] == "private" {
			return sessionKey[:i] + newCtx
		}
	}
	return sessionKey
}
