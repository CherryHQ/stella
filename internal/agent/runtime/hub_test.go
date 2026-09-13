package runtime

import (
	"testing"
	"time"
)

func wakeWithin(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("no wake for %s", what)
	}
}

func noWake(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("unexpected wake for %s", what)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSessionHubWakeOnly(t *testing.T) {
	h := NewSessionHub()
	if h.IsLive("s1") {
		t.Fatal("session should not be live before a turn begins")
	}

	ch, cancel := h.Watch("s1")
	defer cancel()

	h.begin("s1")
	if !h.IsLive("s1") {
		t.Fatal("session should be live after begin")
	}

	// A committed batch wakes the watcher; the channel carries no payload.
	h.wake("s1")
	wakeWithin(t, ch, "committed batch")

	// Wakes coalesce to the cap-1 slot: two wakes leave exactly one pending.
	h.wake("s1")
	h.wake("s1")
	wakeWithin(t, ch, "coalesced wake")
	noWake(t, ch, "second coalesced wake")

	h.end("s1")
	if h.IsLive("s1") {
		t.Fatal("session should not be live after end")
	}
	// The last turn's end wakes once more so a reader notices the terminal
	// marker without waiting out its poll.
	wakeWithin(t, ch, "turn end")
}

func TestSessionHubCancelUnsubscribes(t *testing.T) {
	h := NewSessionHub()
	ch, cancel := h.Watch("s1")

	cancel()
	if _, open := <-ch; open {
		t.Fatal("cancel should close the channel")
	}

	// Waking after cancel must not panic or deliver.
	h.begin("s1")
	h.wake("s1")
	h.end("s1")

	// Double cancel is a no-op.
	cancel()
}

func TestSessionHubWakeNeverBlocks(t *testing.T) {
	h := NewSessionHub()
	_, cancel := h.Watch("s1") // never drained
	defer cancel()

	h.begin("s1")
	for range 10 {
		h.wake("s1")
	}
	h.end("s1")
}
