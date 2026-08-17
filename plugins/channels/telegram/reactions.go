package telegram

import (
	"strconv"

	tele "gopkg.in/telebot.v4"
)

// Reaction lifecycle emoji. 🤔 marks a message as picked up and is cleared
// again when the answer lands, matching what the Feishu channel does. Only a
// failure leaves a mark behind.
//
// Every Telegram reaction plays an animation when it is set, so a three-state
// 👀 → 👍/👎 lifecycle fires two animations per turn on a message the user is
// already watching. Success needs no emoji: the reply itself is the signal.
//
// Telegram also only accepts emoji from a fixed allowlist. ✅/❌ (what the
// Discord plugin uses) are not on it, which is why failure is 👎. An off-list
// emoji makes the API reject the call, so do not "align" these with Discord's
// without checking the allowlist first.
const (
	reactionReceived = "🤔"
	reactionFailure  = "👎"
)

// react sets the bot's reaction on a message, replacing whatever it set before.
// An empty emoji clears it. Unlike Discord, setMessageReaction overwrites the
// bot's entire reaction set in one call, so no removal step is needed and
// reactions left by other users are never disturbed.
//
// Reactions are UI polish, not delivery: failures are logged and swallowed so
// they can never fail the turn that triggered them.
func (b *Bot) react(chatID, messageID, emoji string) error {
	if b.bot == nil || chatID == "" || messageID == "" {
		return nil
	}
	var reactions []tele.Reaction
	if emoji != "" {
		reactions = []tele.Reaction{{Type: tele.ReactionTypeEmoji, Emoji: emoji}}
	}
	err := b.bot.React(chatRef(chatID), tele.StoredMessage{MessageID: messageID}, tele.Reactions{
		Reactions: reactions,
	})
	return err
}

// finishReaction clears the acknowledgement on success, or replaces it with a
// failure mark so a turn that produced nothing useful is still distinguishable
// from one that is still running.
func (b *Bot) finishReaction(chatID, messageID string, success bool) error {
	emoji := ""
	if !success {
		emoji = reactionFailure
	}
	return b.react(chatID, messageID, emoji)
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
