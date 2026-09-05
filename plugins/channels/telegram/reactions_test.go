package telegram

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	tele "gopkg.in/telebot.v4"

	"github.com/CherryHQ/stella/pkg/channel"
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

// reactionEmoji extracts the emoji from a recorded setMessageReaction call, or
// "" when the call clears the reaction.
func reactionEmoji(t *testing.T, call telegramAPICall) string {
	t.Helper()
	raw, ok := call.params["reaction"].(string)
	if !ok {
		t.Fatalf("setMessageReaction carried no reaction payload: %#v", call.params)
	}
	var reactions []tele.Reaction
	if err := json.Unmarshal([]byte(raw), &reactions); err != nil {
		t.Fatalf("decode reaction payload %q: %v", raw, err)
	}
	if len(reactions) == 0 {
		return ""
	}
	if len(reactions) != 1 {
		t.Fatalf("reaction payload = %#v, want at most one emoji", reactions)
	}
	if reactions[0].Type != tele.ReactionTypeEmoji {
		t.Fatalf("reaction type = %q, want %q", reactions[0].Type, tele.ReactionTypeEmoji)
	}
	return reactions[0].Emoji
}

func TestPublishAcknowledgesThenClearsGroupTurn(t *testing.T) {
	fake := &telegramAPIFake{}
	b := newPublisherTestBot(t, fake)
	events := make(chan channel.Event, 1)
	events <- channel.Event{Text: "done"}
	close(events)

	err := b.Publish(context.Background(), channel.GroupPublishRequest{
		PlatformGroupID: "-100",
		ReplyTo:         "7",
		Stream:          &channel.ChatStream{Events: events},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	calls := fake.callsFor("setMessageReaction")
	if len(calls) != 2 {
		t.Fatalf("setMessageReaction calls = %d, want acknowledgement plus verdict", len(calls))
	}
	if got := reactionEmoji(t, calls[0]); got != reactionReceived {
		t.Errorf("first reaction = %q, want %q", got, reactionReceived)
	}
	// Success clears the mark rather than adding a second one: the reply is the
	// signal, and every extra reaction costs another animation.
	if got := reactionEmoji(t, calls[1]); got != "" {
		t.Errorf("terminal reaction = %q, want the acknowledgement cleared", got)
	}
	if got := calls[0].params["chat_id"]; got != "-100" {
		t.Errorf("reaction chat_id = %#v, want the group", got)
	}
	if got := calls[0].params["message_id"]; got != "7" {
		t.Errorf("reaction message_id = %#v, want the triggering message", got)
	}
}

func TestPublishRejectsGroupStreamErrorWithoutReaction(t *testing.T) {
	fake := &telegramAPIFake{}
	b := newPublisherTestBot(t, fake)
	events := make(chan channel.Event, 1)
	events <- channel.Event{Err: context.DeadlineExceeded}
	close(events)

	err := b.Publish(context.Background(), channel.GroupPublishRequest{
		PlatformGroupID: "-100",
		ReplyTo:         "7",
		Stream:          &channel.ChatStream{Events: events},
	})
	if err == nil {
		t.Fatal("Publish unexpectedly accepted a failed replay")
	}

	calls := fake.callsFor("setMessageReaction")
	if len(calls) != 0 {
		t.Fatalf("setMessageReaction calls = %d, want no platform-side effect", len(calls))
	}
}

// A group turn with no triggering message must not react at all, rather than
// react on a message id of "" and log an API error on every turn.
func TestPublishSkipsReactionsWithoutReplyTarget(t *testing.T) {
	fake := &telegramAPIFake{}
	b := newPublisherTestBot(t, fake)
	events := make(chan channel.Event, 1)
	events <- channel.Event{Text: "done"}
	close(events)

	err := b.Publish(context.Background(), channel.GroupPublishRequest{
		PlatformGroupID: "-100",
		Stream:          &channel.ChatStream{Events: events},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if calls := fake.callsFor("setMessageReaction"); len(calls) != 0 {
		t.Fatalf("setMessageReaction calls = %d, want none without a reply target", len(calls))
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
