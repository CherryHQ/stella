package discord

import (
	"context"

	"github.com/bwmarrin/discordgo"
)

// Reaction lifecycle emoji. 👀 marks a message as received; it is replaced by
// ✅ or ❌ once the turn that message triggered reaches a terminal state.
// Adding these needs no reaction gateway intent — that intent only gates
// *receiving* reaction-add events, and nothing here listens for those.
const (
	reactionReceived = "👀"
	reactionSuccess  = "✅"
	reactionFailure  = "❌"
)

// reactBestEffort adds emoji to a message. Reactions are UI polish, not
// delivery: a failure here must never bubble up and cause the caller (a
// message handler or the group dispatcher) to retry the underlying turn.
func (b *Bot) reactBestEffort(ctx context.Context, channelID, messageID, emoji string) {
	if b.rest == nil || channelID == "" || messageID == "" {
		return
	}
	if err := b.rest.MessageReactionAdd(channelID, messageID, emoji, discordgo.WithContext(ctx)); err != nil {
		logger().Debug("add discord reaction failed", "channel_id", channelID, "message_id", messageID, "emoji", emoji, "error", err)
	}
}

// finishReaction transitions a message's lifecycle reaction from 👀 to a
// terminal ✅/❌. It removes only the bot's own 👀 (via "@me"), never a
// remove-all, so it cannot erase a human's unrelated reactions on the message.
func (b *Bot) finishReaction(ctx context.Context, channelID, messageID string, success bool) {
	if b.rest == nil || channelID == "" || messageID == "" {
		return
	}
	if err := b.rest.MessageReactionRemove(channelID, messageID, reactionReceived, "@me", discordgo.WithContext(ctx)); err != nil {
		logger().Debug("remove discord ack reaction failed", "channel_id", channelID, "message_id", messageID, "error", err)
	}
	emoji := reactionSuccess
	if !success {
		emoji = reactionFailure
	}
	b.reactBestEffort(ctx, channelID, messageID, emoji)
}
