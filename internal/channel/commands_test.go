package channel

import (
	"context"
	"strings"
	"testing"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

func TestHandleCommandHelp(t *testing.T) {
	rc := &ResolvedChat{SessionKey: "ch"}
	resp, ok := HandleCommand(context.Background(), rc, "/help", "user1")
	if !ok {
		t.Fatal("expected handled")
	}
	if resp != pkgchannel.WelcomeMessage {
		t.Errorf("response = %q", resp)
	}
}

func TestHandleCommandStart(t *testing.T) {
	rc := &ResolvedChat{SessionKey: "ch"}
	resp, ok := HandleCommand(context.Background(), rc, "/start", "user1")
	if !ok {
		t.Fatal("expected handled")
	}
	if resp != pkgchannel.WelcomeMessage {
		t.Errorf("response = %q", resp)
	}
}

func TestHandleCommandWhoami(t *testing.T) {
	rc := &ResolvedChat{SessionKey: "ch"}
	resp, ok := HandleCommand(context.Background(), rc, "/whoami", "SENDER_123")
	if !ok {
		t.Fatal("expected handled")
	}
	if resp != "Your ID: SENDER_123" {
		t.Errorf("response = %q", resp)
	}
}

func TestHandleCommandUnknown(t *testing.T) {
	rc := &ResolvedChat{SessionKey: "ch"}
	_, ok := HandleCommand(context.Background(), rc, "hello world", "user1")
	if ok {
		t.Error("regular text should not be handled")
	}
}

func TestHandleCommandEmpty(t *testing.T) {
	rc := &ResolvedChat{SessionKey: "ch"}
	_, ok := HandleCommand(context.Background(), rc, "", "user1")
	if ok {
		t.Error("empty text should not be handled")
	}
}

func TestHandleCommandModel(t *testing.T) {
	rc := &ResolvedChat{SessionKey: "ch"}
	_, ok := HandleCommand(context.Background(), rc, "/model gpt-4", "user1")
	if ok {
		t.Error("/model should NOT be handled (left to channels)")
	}
}

func TestWelcomeMessageIncludesNaturalLanguagePhrases(t *testing.T) {
	// Verify the WelcomeMessage explains natural-language shortcuts
	if !strings.Contains(pkgchannel.WelcomeMessage, "new session") {
		t.Error("WelcomeMessage should mention 'new session' as natural-language phrase")
	}
	if !strings.Contains(pkgchannel.WelcomeMessage, "新会话") {
		t.Error("WelcomeMessage should mention Chinese examples like '新会话'")
	}
	if !strings.Contains(pkgchannel.WelcomeMessage, "取消") {
		t.Error("WelcomeMessage should mention Chinese abort examples like '取消'")
	}
	if !strings.Contains(pkgchannel.WelcomeMessage, "If a short phrase is unclear") {
		t.Error("WelcomeMessage should explain the fallback behavior for unclear phrases")
	}
}
