package server

import (
	"net/http/httptest"
	"testing"

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
	ok, truncated := s.streamDurableTurn(t.Context(), w, w, "agent", "session", sessionaccess.AttachResult{}, 0)
	if ok || truncated {
		t.Fatal("disabled durable mode must leave idle SSE on its existing 204 path")
	}
}
