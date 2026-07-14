package server

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/ai"
)

type startNotifyingRecorder struct {
	*httptest.ResponseRecorder
	start chan struct{}
	once  sync.Once
}

func (w *startNotifyingRecorder) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	if bytes.Contains(p, []byte(`"type":"start"`)) {
		w.once.Do(func() { close(w.start) })
	}
	return n, err
}

func TestStreamAgentEventsIdleDoesNotCallDeliveryGuard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan agent.Event)
	rr := &startNotifyingRecorder{ResponseRecorder: httptest.NewRecorder(), start: make(chan struct{})}
	var begins atomic.Int64
	done := make(chan struct{})
	go func() {
		streamAgentEvents(ctx, rr, rr, "a1", "s1", ch, func() error {
			begins.Add(1)
			return nil
		})
		close(done)
	}()
	select {
	case <-rr.start:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not become idle")
	}
	if got := begins.Load(); got != 0 {
		t.Fatalf("delivery-time Begins while idle=%d, want 0", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
}

func TestStreamAgentEventsDenialDoesNotEncodeProtectedSourceEvent(t *testing.T) {
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Store: ai.UserMessage{Content: "stored"}}
	ch <- agent.Event{Err: errors.New("secret source error")}
	close(ch)

	rr := httptest.NewRecorder()
	guardCalls := 0
	streamAgentEvents(context.Background(), rr, rr, "a1", "s1", ch, func() error {
		guardCalls++
		return errors.New("revoked")
	})

	body := rr.Body.String()
	if guardCalls != 1 {
		t.Fatalf("guard calls=%d, want 1", guardCalls)
	}
	if !strings.Contains(body, `"type":"start"`) {
		t.Fatalf("stream did not write initial start event: %q", body)
	}
	for _, forbidden := range []string{"secret", "text-delta", "errorText", "[DONE]"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("stream encoded %q after denial: %q", forbidden, body)
		}
	}
}
