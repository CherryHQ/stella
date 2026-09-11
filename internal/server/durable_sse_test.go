package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
)

func TestLegacyIdleSSEWithoutEventStore(t *testing.T) {
	defer func() {
		if v := recover(); v != nil {
			t.Fatalf("flag-off idle SSE panicked instead of returning no live turn: %v", v)
		}
	}()
	s := &Server{}
	w := httptest.NewRecorder()
	ok, truncated := s.streamDurableTurn(t.Context(), w, w, "agent", "session", sessionaccess.AttachResult{}, "")
	if ok || truncated {
		t.Fatal("disabled durable mode must leave idle SSE on its existing 204 path")
	}
}

// The cursor is committed only after every frame of the durable event is
// flushed: the `id:` line must come after the event's delta, never before.
func TestCursorEmittedAfterEventFrames(t *testing.T) {
	ch := make(chan agent.Event, 1)
	ch <- agent.Event{Seq: 7, DurableID: "run-x:7", Text: "irreplaceable delta"}
	close(ch)
	w := httptest.NewRecorder()
	streamAgentEvents(t.Context(), w, w, "a", "s", ch, nil)
	wire := w.Body.String()
	delta := strings.Index(wire, "irreplaceable delta")
	cursor := strings.Index(wire, "id: run-x:7")
	if delta < 0 || cursor < 0 {
		t.Fatalf("wire missing delta or cursor:\n%s", wire)
	}
	if cursor < delta {
		t.Fatal("cursor emitted before the event's delta — a disconnect would skip unsent frames")
	}
}
