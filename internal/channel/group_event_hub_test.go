package channel

import "testing"

// A subscriber that stops reading must be dropped exactly once. Turn events
// carry no AppendResult, so a drop keyed on the embedded group id would leave
// the closed channel registered and panic the next broadcast.
func TestGroupEventHubDropsStalledTurnSubscriberOnce(t *testing.T) {
	hub := NewGroupEventHub()
	ch, cancel := hub.Subscribe("group-1")
	for range cap(ch) + 4 {
		hub.AnnounceTurn("group-1", "agent-1", "thinking", "")
	}
	hub.AnnounceTurn("group-1", "agent-1", "silent", "done")

	hub.mu.Lock()
	remaining := len(hub.subs["group-1"])
	hub.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("stalled subscribers=%d, want 0", remaining)
	}
	cancel()
}

func TestGroupEventHubDeliversTurnAndMessageEvents(t *testing.T) {
	hub := NewGroupEventHub()
	ch, cancel := hub.Subscribe("group-1")
	defer cancel()

	hub.AnnounceTurn("group-1", "agent-1", "held", "freshness")
	hub.AnnounceTurn("group-2", "agent-2", "held", "freshness")

	event := <-ch
	if event.Turn == nil || event.Turn.AgentID != "agent-1" || event.Turn.State != "held" {
		t.Fatalf("first event=%+v, want agent-1 held", event.Turn)
	}
	select {
	case leaked := <-ch:
		t.Fatalf("received event for another group: %+v", leaked)
	default:
	}
}
