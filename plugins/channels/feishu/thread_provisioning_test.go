package feishu

import (
	"context"
	"errors"
	"testing"
)

func TestThreadProvisionFailureLeavesMessageEligibleForRedelivery(t *testing.T) {
	bot, handler, captured := newThreadRoutingBotWithHandler(t)
	attempts := 0
	handler.ensureThreadGroupMemberFn = func(context.Context, string, string, string, string, string) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary database failure")
		}
		return nil
	}
	event := textReceiveEvent("oc_parent", "group", "om_redelivered", "om_root", "", "hello")
	if err := bot.onMessage(context.Background(), event); err == nil {
		t.Fatal("first provision failure unexpectedly succeeded")
	}
	if err := bot.onMessage(context.Background(), event); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	_ = waitMessage(t, captured)
	if attempts != 2 {
		t.Fatalf("provision attempts = %d, want 2 after redelivery", attempts)
	}
}

func TestOnMessageProvisionsThreadGroupMemberOncePerThread(t *testing.T) {
	bot, handler, captured := newThreadRoutingBotWithHandler(t)
	type provision struct {
		platform string
		chatID   string
		rootID   string
		legacyID string
		channel  string
	}
	var provisions []provision
	handler.ensureThreadGroupMemberFn = func(_ context.Context, platform, chatID, rootID, legacyID, channelID string) error {
		provisions = append(provisions, provision{platform, chatID, rootID, legacyID, channelID})
		return nil
	}

	for _, messageID := range []string{"om_first", "om_second"} {
		if err := bot.onMessage(context.Background(), textReceiveEvent("oc_parent", "group", messageID, "om_root", "", "hello")); err != nil {
			t.Fatalf("onMessage(%s): %v", messageID, err)
		}
		_ = waitMessage(t, captured)
	}
	if len(provisions) != 1 {
		t.Fatalf("thread provisions = %#v, want one cached provision", provisions)
	}
	got := provisions[0]
	if got.platform != "feishu" || got.chatID != "oc_parent" || got.rootID != "om_root" || got.legacyID != "" || got.channel != "feishu" {
		t.Fatalf("thread provision = %#v, want Feishu (parent, root) with no legacy adoption", got)
	}

	if err := bot.onMessage(context.Background(), textReceiveEvent("oc_parent", "group", "om_other", "om_other_root", "", "other thread")); err != nil {
		t.Fatal(err)
	}
	_ = waitMessage(t, captured)
	if len(provisions) != 2 || provisions[1].rootID != "om_other_root" {
		t.Fatalf("thread provisions = %#v, want separate member for second thread", provisions)
	}

	if err := bot.onMessage(context.Background(), textReceiveEvent("oc_parent", "group", "om_parent_message", "", "", "parent")); err != nil {
		t.Fatal(err)
	}
	_ = waitMessage(t, captured)
	if len(provisions) != 2 {
		t.Fatalf("parent message provisioned a thread: %#v", provisions)
	}
}
