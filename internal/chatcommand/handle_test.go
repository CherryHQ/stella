package chatcommand

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/chatroute"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

func TestHandleHelp(t *testing.T) {
	rc := &chatroute.ResolvedChat{SessionKey: "ch"}
	resp, ok := Handle(context.Background(), rc, "/help", "user1")
	if !ok {
		t.Fatal("expected handled")
	}
	if resp != pkgchannel.WelcomeMessage {
		t.Errorf("response = %q", resp)
	}
}

func TestHandleStart(t *testing.T) {
	rc := &chatroute.ResolvedChat{SessionKey: "ch"}
	resp, ok := Handle(context.Background(), rc, "/start", "user1")
	if !ok {
		t.Fatal("expected handled")
	}
	if resp != pkgchannel.WelcomeMessage {
		t.Errorf("response = %q", resp)
	}
}

func TestHandleWhoami(t *testing.T) {
	rc := &chatroute.ResolvedChat{SessionKey: "ch"}
	resp, ok := Handle(context.Background(), rc, "/whoami", "SENDER_123")
	if !ok {
		t.Fatal("expected handled")
	}
	if resp != "Your ID: SENDER_123" {
		t.Errorf("response = %q", resp)
	}
}

func TestHandleUnknown(t *testing.T) {
	rc := &chatroute.ResolvedChat{SessionKey: "ch"}
	_, ok := Handle(context.Background(), rc, "hello world", "user1")
	if ok {
		t.Error("regular text should not be handled")
	}
}

func TestHandleEmpty(t *testing.T) {
	rc := &chatroute.ResolvedChat{SessionKey: "ch"}
	_, ok := Handle(context.Background(), rc, "", "user1")
	if ok {
		t.Error("empty text should not be handled")
	}
}

func TestHandleModel(t *testing.T) {
	rc := &chatroute.ResolvedChat{SessionKey: "ch"}
	_, ok := Handle(context.Background(), rc, "/model gpt-4", "user1")
	if ok {
		t.Error("/model should NOT be handled (left to channels)")
	}
}
