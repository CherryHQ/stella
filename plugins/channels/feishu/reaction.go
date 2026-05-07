package feishu

import (
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Emoji type constants for message reactions.
// Full type list: https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message-reaction/emojis-introduce
const (
	reactionAck   = "THINKING"  // 🤔 — message received, processing
	reactionError = "CrossMark" // ❌ — processing failed
)

// reactToMessage adds an emoji reaction to a message and returns the reaction
// ID needed for later deletion. Returns "" on failure (logged, never fatal).
func (b *Bot) reactToMessage(messageID, emojiType string) string {
	if b.client == nil || messageID == "" {
		return ""
	}
	apiCtx, cancel := b.apiContext()
	defer cancel()

	emoji := larkim.NewEmojiBuilder().EmojiType(emojiType).Build()
	resp, err := b.client.Im.MessageReaction.Create(apiCtx,
		larkim.NewCreateMessageReactionReqBuilder().
			MessageId(messageID).
			Body(larkim.NewCreateMessageReactionReqBodyBuilder().
				ReactionType(emoji).
				Build()).
			Build())
	if err != nil {
		logger().Warn("react to message failed", "message_id", messageID, "emoji", emojiType, "error", err)
		return ""
	}
	if !resp.Success() {
		logger().Warn("react to message failed", "message_id", messageID, "emoji", emojiType, "code", resp.Code, "msg", resp.Msg)
		return ""
	}
	if resp.Data != nil && resp.Data.ReactionId != nil {
		return *resp.Data.ReactionId
	}
	return ""
}

// removeReaction deletes a previously added reaction. Failures are logged and ignored.
func (b *Bot) removeReaction(messageID, reactionID string) {
	if b.client == nil || messageID == "" || reactionID == "" {
		return
	}
	apiCtx, cancel := b.apiContext()
	defer cancel()

	resp, err := b.client.Im.MessageReaction.Delete(apiCtx,
		larkim.NewDeleteMessageReactionReqBuilder().
			MessageId(messageID).
			ReactionId(reactionID).
			Build())
	if err != nil {
		logger().Warn("remove reaction failed", "message_id", messageID, "reaction_id", reactionID, "error", err)
		return
	}
	if !resp.Success() {
		logger().Warn("remove reaction failed", "message_id", messageID, "reaction_id", reactionID, "code", resp.Code, "msg", resp.Msg)
	}
}
