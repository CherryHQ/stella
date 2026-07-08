package server

import (
	"testing"
	"time"
)

func TestWebhookLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := newWebhookLimiter(1, 3) // 1 tok/s, burst 3
	l.now = func() time.Time { return now }

	// Burst of 3 is allowed, the 4th is denied.
	for i := range 3 {
		if !l.allow("wh1") {
			t.Fatalf("call %d should be allowed within burst", i+1)
		}
	}
	if l.allow("wh1") {
		t.Fatal("4th call should be denied (bucket empty)")
	}

	// A different webhook has its own bucket.
	if !l.allow("wh2") {
		t.Fatal("independent webhook should be allowed")
	}

	// After 2s, ~2 tokens refill.
	now = now.Add(2 * time.Second)
	refilled := 0
	for l.allow("wh1") {
		refilled++
	}
	if refilled != 2 {
		t.Fatalf("expected ~2 tokens to refill after 2s, got %d", refilled)
	}
}
