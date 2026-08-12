package agent

import (
	"strings"
	"testing"
)

func TestBuildGroupSessionKey(t *testing.T) {
	key := BuildGroupSessionKey("agent1", "group-abc-123")
	if key != "agent1:group:group-abc-123" {
		t.Fatalf("got %q, want %q", key, "agent1:group:group-abc-123")
	}
}

func TestGroupSessionKeyOmitsSenderID(t *testing.T) {
	key := BuildGroupSessionKey("agent1", "group-abc-123")
	if strings.Contains(key, "user") || strings.Contains(key, "sender") {
		t.Fatalf("group session key should not contain user/sender id, got %q", key)
	}
}

func TestTwoSendersShareGroupSessionKey(t *testing.T) {
	keyA := BuildGroupSessionKey("agent1", "group-abc-123")
	keyB := BuildGroupSessionKey("agent1", "group-abc-123")
	if keyA != keyB {
		t.Fatalf("two senders in the same group should produce the same session key: %q vs %q", keyA, keyB)
	}
}
