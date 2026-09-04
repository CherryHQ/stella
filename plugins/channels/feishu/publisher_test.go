package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/channel"
)

func TestPublishReturnsCancellationThatRacesFinalPatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan channel.Event)
	close(events)
	bot := &Bot{
		replyCardFn: func(context.Context, string, string) (string, error) {
			return "om_progress", nil
		},
		patchCardFn: func(context.Context, string, string) error {
			cancel()
			return nil
		},
	}
	err := bot.Publish(ctx, internalchannel.GroupPublishRequest{
		PlatformGroupID: "feishu:oc_group",
		ReplyTo:         "om_request",
		Stream:          &channel.ChatStream{Events: events},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish() error = %v, want context.Canceled", err)
	}
}

func TestPublishReturnsLostLeaseCancellationWithoutSendingTerminalReply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan channel.Event, 1)
	events <- channel.Event{Err: context.Canceled}
	close(events)
	bot := &Bot{
		replyCardFn: func(context.Context, string, string) (string, error) {
			cancel()
			return "om_progress", nil
		},
		patchCardFn: func(context.Context, string, string) error {
			t.Fatal("lost lease must not patch a terminal response")
			return nil
		},
	}
	err := bot.Publish(ctx, internalchannel.GroupPublishRequest{
		PlatformGroupID: "feishu:oc_group",
		ReplyTo:         "om_request",
		Stream:          &channel.ChatStream{Events: events},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish() error = %v, want context.Canceled", err)
	}
}

func TestStreamDoesNotRenderCancelButton(t *testing.T) {
	var content string
	bot := &Bot{
		replyCardFn: func(_ context.Context, _ string, card string) (string, error) {
			content = card
			return "om_progress", nil
		},
	}
	events := make(chan channel.Event)
	close(events)
	messageID, _, _, _, _, _, err := bot.streamResponseInThread(context.Background(), events, "oc_chat", "om_request", "om_root")
	if err != nil || messageID != "om_progress" {
		t.Fatalf("stream start = message %q, err %v", messageID, err)
	}
	if strings.Contains(content, "\"content\":\"Cancel\"") {
		t.Fatalf("thinking card included a cancel button: %s", content)
	}
}

// A group turn woken by a peer's post or by a stall nudge has no platform
// message behind it. The Reply API rejects an empty message_id, so the reply
// must go to the chat instead of failing delivery outright.
func TestFinalResponseWithoutReplyTargetPostsToChat(t *testing.T) {
	var gotChat, gotType string
	bot := &Bot{
		createMessageFn: func(_ context.Context, chatID, msgType, _ string) (string, error) {
			gotChat, gotType = chatID, msgType
			return "om_new", nil
		},
		replyCardFn: func(context.Context, string, string) (string, error) {
			t.Fatal("reply API used with no message to reply to")
			return "", nil
		},
	}
	if err := bot.sendFinalResponseInThread(context.Background(), "oc_chat", "", "", "", "hello", nil, true, true); err != nil {
		t.Fatalf("sendFinalResponseInThread: %v", err)
	}
	if gotChat != "oc_chat" {
		t.Errorf("chat id = %q, want oc_chat", gotChat)
	}
	if gotType != larkim.MsgTypeInteractive {
		t.Errorf("msg type = %q, want interactive", gotType)
	}
}
