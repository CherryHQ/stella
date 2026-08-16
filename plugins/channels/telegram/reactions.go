package telegram

import (
	"strconv"

	tele "gopkg.in/telebot.v4"
)

// Reaction lifecycle emoji. 👀 marks a message as picked up; it is replaced by
// 👍 or 👎 once the turn that message triggered reaches a terminal state.
//
// Telegram only accepts emoji from a fixed allowlist, and ✅/❌ (what the
// Discord plugin uses) are not on it — 👍/👎 are the closest members. An
// off-list emoji makes the API reject the call, so do not "align" these with
// Discord's without checking the allowlist first.
const (
	reactionReceived = "👀"
	reactionSuccess  = "👍"
	reactionFailure  = "👎"
)

// react sets the bot's reaction on a message, replacing whatever it set before.
// Unlike Discord, setMessageReaction overwrites the bot's entire reaction set in
// one call, so transitioning 👀 to a terminal emoji needs no removal step and
// cannot disturb reactions left by other users.
//
// Reactions are UI polish, not delivery: failures are logged and swallowed so
// they can never fail the turn that triggered them.
func (b *Bot) react(chatID, messageID, emoji string) {
	if b.bot == nil || chatID == "" || messageID == "" {
		return
	}
	err := b.bot.React(chatRef(chatID), tele.StoredMessage{MessageID: messageID}, tele.Reactions{
		Reactions: []tele.Reaction{{Type: tele.ReactionTypeEmoji, Emoji: emoji}},
	})
	if err != nil {
		logger().Debug("set telegram reaction failed", "chat_id", chatID, "message_id", messageID, "emoji", emoji, "error", err)
	}
}

// finishReaction replaces the 👀 acknowledgement with a terminal verdict.
func (b *Bot) finishReaction(chatID, messageID string, success bool) {
	emoji := reactionSuccess
	if !success {
		emoji = reactionFailure
	}
	b.react(chatID, messageID, emoji)
}

// reactionTarget extracts the chat and message identifiers to react on, or
// empty strings when the update carries no reactable message.
func reactionTarget(c tele.Context) (chatID, messageID string) {
	m := c.Message()
	if m == nil || m.ID == 0 || c.Chat() == nil {
		return "", ""
	}
	return strconv.FormatInt(c.Chat().ID, 10), strconv.Itoa(m.ID)
}
