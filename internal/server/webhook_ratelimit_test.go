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

func TestWebhookLimiterInflight(t *testing.T) {
	l := newWebhookLimiter(1, 1)
	l.maxInflight = 2

	for i := range 2 {
		if !l.beginRun("wh1") {
			t.Fatalf("run %d should acquire a slot", i+1)
		}
	}
	if l.beginRun("wh1") {
		t.Fatal("third concurrent run should be rejected")
	}
	// Other webhooks are unaffected.
	if !l.beginRun("wh2") {
		t.Fatal("independent webhook should acquire a slot")
	}

	// Releasing a slot lets the next run in; a fully drained key is evicted.
	l.endRun("wh1")
	if !l.beginRun("wh1") {
		t.Fatal("released slot should be reusable")
	}
	l.endRun("wh1")
	l.endRun("wh1")
	l.endRun("wh2")
	if len(l.inflight) != 0 {
		t.Fatalf("drained keys should be evicted, got %v", l.inflight)
	}
}

func TestWebhookLimiterRemoveClearsDeletedResourceState(t *testing.T) {
	l := newWebhookLimiter(1, 1)
	if !l.allow("deleted") || !l.acquireIngress("deleted") || !l.beginRun("deleted") {
		t.Fatal("failed to seed limiter state")
	}
	l.remove("deleted")
	if len(l.buckets) != 0 || len(l.ingress) != 0 || len(l.inflight) != 0 {
		t.Fatalf("remove left state: buckets=%d ingress=%d inflight=%d", len(l.buckets), len(l.ingress), len(l.inflight))
	}
	// Late releases from work admitted before deletion are harmless.
	l.releaseIngress("deleted")
	l.endRun("deleted")
	if !l.allow("deleted") {
		t.Fatal("a fresh bucket for a non-reused key should start full")
	}
}

func TestWebhookLimiterIngressSlot(t *testing.T) {
	l := newWebhookLimiter(1, 1)
	l.maxIngress = 2

	for i := range 2 {
		if !l.acquireIngress("wh1") {
			t.Fatalf("ingress %d should acquire a slot", i+1)
		}
	}
	if l.acquireIngress("wh1") {
		t.Fatal("third concurrent ingress should be rejected")
	}
	// The ingress slot is independent of the acceptance bucket and the run slot.
	if l.buckets["wh1"] != nil {
		t.Fatal("acquiring an ingress slot must not touch the acceptance bucket")
	}
	if l.inflight["wh1"] != 0 {
		t.Fatal("acquiring an ingress slot must not consume a run slot")
	}
	// Other webhooks are unaffected.
	if !l.acquireIngress("wh2") {
		t.Fatal("independent webhook should acquire an ingress slot")
	}

	// Releasing a slot lets the next in; a fully drained key is evicted.
	l.releaseIngress("wh1")
	if !l.acquireIngress("wh1") {
		t.Fatal("released ingress slot should be reusable")
	}
	l.releaseIngress("wh1")
	l.releaseIngress("wh1")
	l.releaseIngress("wh2")
	if len(l.ingress) != 0 {
		t.Fatalf("drained ingress keys should be evicted, got %v", l.ingress)
	}
}
