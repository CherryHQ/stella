package feishutool

import (
	"context"
	"testing"
)

func TestWithOpenID(t *testing.T) {
	ctx := context.Background()
	if got := OpenIDFromContext(ctx); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	ctx = WithOpenID(ctx, "ou_abc123")
	if got := OpenIDFromContext(ctx); got != "ou_abc123" {
		t.Fatalf("expected ou_abc123, got %q", got)
	}
}

func TestWithChatID(t *testing.T) {
	ctx := context.Background()
	if got := ChatIDFromContext(ctx); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	ctx = WithChatID(ctx, "oc_chat456")
	if got := ChatIDFromContext(ctx); got != "oc_chat456" {
		t.Fatalf("expected oc_chat456, got %q", got)
	}
}

func TestWithMessageID(t *testing.T) {
	ctx := context.Background()
	if got := MessageIDFromContext(ctx); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	ctx = WithMessageID(ctx, "om_msg789")
	if got := MessageIDFromContext(ctx); got != "om_msg789" {
		t.Fatalf("expected om_msg789, got %q", got)
	}
}

func TestContextKeysIndependent(t *testing.T) {
	ctx := context.Background()
	ctx = WithOpenID(ctx, "ou_1")
	ctx = WithChatID(ctx, "oc_2")
	ctx = WithMessageID(ctx, "om_3")

	if got := OpenIDFromContext(ctx); got != "ou_1" {
		t.Fatalf("open_id: expected ou_1, got %q", got)
	}
	if got := ChatIDFromContext(ctx); got != "oc_2" {
		t.Fatalf("chat_id: expected oc_2, got %q", got)
	}
	if got := MessageIDFromContext(ctx); got != "om_3" {
		t.Fatalf("message_id: expected om_3, got %q", got)
	}
}
