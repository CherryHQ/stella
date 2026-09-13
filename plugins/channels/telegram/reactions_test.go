package telegram

import (
	"slices"
	"testing"

	tele "gopkg.in/telebot.v4"
)

// telegramAllowedReactions is the emoji set Telegram accepts for
// setMessageReaction. Anything outside it is rejected at the API, so the
// lifecycle constants are pinned against this list rather than against what the
// other channel plugins happen to use.
// Source: https://core.telegram.org/bots/api#reactiontypeemoji
var telegramAllowedReactions = []string{
	"👍", "👎", "❤", "🔥", "🥰", "👏", "😁", "🤔", "🤯", "😱", "🤬", "😢",
	"🎉", "🤩", "🤮", "💩", "🙏", "👌", "🕊", "🤡", "🥱", "🥴", "😍", "🐳",
	"❤‍🔥", "🌚", "🌭", "💯", "🤣", "⚡", "🍌", "🏆", "💔", "🤨", "😐", "🍓",
	"🍾", "💋", "🖕", "😈", "😴", "😭", "🤓", "👻", "👨‍💻", "👀", "🎃", "🙈",
	"😇", "😨", "🤝", "✍", "🤗", "🫡", "🎅", "🎄", "☃", "💅", "🤪", "🗿",
	"🆒", "💘", "🙉", "🦄", "😘", "💊", "🙊", "😎", "👾", "🤷‍♂", "🤷", "🤷‍♀", "😡",
}

func TestReactionEmojiAreOnTelegramAllowlist(t *testing.T) {
	for _, emoji := range []string{reactionReceived, reactionFailure} {
		if !slices.Contains(telegramAllowedReactions, emoji) {
			t.Errorf("reaction %q is not on Telegram's allowlist; setMessageReaction would reject it", emoji)
		}
	}
}

func TestReactionTargetIgnoresUpdatesWithoutMessage(t *testing.T) {
	fake := &telegramAPIFake{}
	b := newPublisherTestBot(t, fake)

	chatID, messageID := reactionTarget(b.bot.NewContext(tele.Update{}))
	if chatID != "" || messageID != "" {
		t.Fatalf("reactionTarget(empty update) = %q/%q, want empty", chatID, messageID)
	}

	ctx := b.bot.NewContext(tele.Update{Message: &tele.Message{
		ID:   11,
		Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup},
	}})
	chatID, messageID = reactionTarget(ctx)
	if chatID != "-100" || messageID != "11" {
		t.Fatalf("reactionTarget = %q/%q, want -100/11", chatID, messageID)
	}
}
