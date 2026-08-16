package feishu

import (
	"context"
	"errors"
	"testing"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/channel"
)

func TestPublishReturnsCancellationThatRacesFinalPatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan channel.Event)
	close(events)
	bot := &Bot{
		cancels: newCancelRegistry(),
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
		cancels: newCancelRegistry(),
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
