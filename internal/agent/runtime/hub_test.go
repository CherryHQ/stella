package runtime

import "testing"

func TestSessionHubFanOutAndClose(t *testing.T) {
	h := NewSessionHub()
	if h.IsLive("s1") {
		t.Fatal("session should not be live before a turn begins")
	}

	ch, cancel := h.Subscribe("s1")
	defer cancel()

	h.begin("s1")
	if !h.IsLive("s1") {
		t.Fatal("session should be live after begin")
	}

	h.publish("s1", Event{Text: "hello"})
	if ev := <-ch; ev.Text != "hello" {
		t.Fatalf("got %q, want %q", ev.Text, "hello")
	}

	h.end("s1")
	if h.IsLive("s1") {
		t.Fatal("session should not be live after end")
	}
	if _, open := <-ch; open {
		t.Fatal("subscriber channel should be closed when the turn ends")
	}
}

func TestSessionHubReplaysActiveTurnBeforeLiveEvents(t *testing.T) {
	h := NewSessionHub()
	h.begin("s1")
	h.publish("s1", Event{Text: "before"})

	ch, cancel := h.Subscribe("s1")
	defer cancel()
	h.publish("s1", Event{Text: "after"})

	if event := <-ch; event.Text != "before" {
		t.Fatalf("first event = %q, want replayed event", event.Text)
	}
	if event := <-ch; event.Text != "after" {
		t.Fatalf("second event = %q, want live event", event.Text)
	}
	h.end("s1")
}

func TestSessionHubReplayCeilingFailsClosed(t *testing.T) {
	h := NewSessionHub()
	h.begin("s1")
	h.publish("s1", Event{Text: string(make([]byte, replayMaxBytes+1))})

	ch, cancel := h.Subscribe("s1")
	defer cancel()
	select {
	case event := <-ch:
		t.Fatalf("oversized replay unexpectedly delivered: %d bytes", len(event.Text))
	default:
	}
	h.end("s1")
}

func TestSessionHubCoalescesTextBeforeEventCeiling(t *testing.T) {
	h := NewSessionHub()
	h.begin("s1")
	for range replayMaxEvents + 1 {
		h.publish("s1", Event{Text: "x"})
	}

	ch, cancel := h.Subscribe("s1")
	defer cancel()
	if event := <-ch; len(event.Text) != replayMaxEvents+1 {
		t.Fatalf("coalesced replay length = %d, want %d", len(event.Text), replayMaxEvents+1)
	}
	h.end("s1")
}

func TestSessionHubEventCeilingFailsClosed(t *testing.T) {
	h := NewSessionHub()
	h.begin("s1")
	for range replayMaxEvents + 1 {
		h.publish("s1", Event{Step: &StepEvent{Kind: "start"}})
	}

	ch, cancel := h.Subscribe("s1")
	defer cancel()
	select {
	case event := <-ch:
		t.Fatalf("event-count overflow unexpectedly replayed: %#v", event)
	default:
	}
	h.end("s1")
}

func TestSessionHubCancelUnsubscribes(t *testing.T) {
	h := NewSessionHub()
	ch, cancel := h.Subscribe("s1")

	cancel()
	if _, open := <-ch; open {
		t.Fatal("cancel should close the channel")
	}

	// Publishing after cancel must not panic or deliver.
	h.begin("s1")
	h.publish("s1", Event{Text: "dropped"})
	h.end("s1")

	// Double cancel is a no-op.
	cancel()
}

func TestSessionHubPublishNeverBlocks(t *testing.T) {
	h := NewSessionHub()
	_, cancel := h.Subscribe("s1") // never drained
	defer cancel()

	// More events than the buffer; excess is dropped rather than blocking.
	for range subBuffer + 10 {
		h.publish("s1", Event{Text: "x"})
	}
}
