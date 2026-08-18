package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/CherryHQ/stella/internal/channel"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type failingStreamWriter struct {
	headers http.Header
	writes  int
}

func (w *failingStreamWriter) Header() http.Header {
	if w.headers == nil {
		w.headers = make(http.Header)
	}
	return w.headers
}

func (w *failingStreamWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("client gone")
}

func (w *failingStreamWriter) WriteHeader(int) {}
func (w *failingStreamWriter) Flush()          {}

func TestWebGroupPublisherWriteFailureDrainsStream(t *testing.T) {
	events := make(chan pkgchannel.Event)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		events <- pkgchannel.Event{Text: "one"}
		events <- pkgchannel.Event{Text: "two"}
		close(events)
	}()

	w := &failingStreamWriter{}
	publisher := &webGroupPublisher{w: w, flusher: w}
	err := publisher.Publish(context.Background(), channel.GroupPublishRequest{
		AgentID:   "agent-1",
		AgentName: "Agent One",
		Stream:    &pkgchannel.ChatStream{Events: events, SessionID: "session-1"},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	<-drained
	if !publisher.clientGone {
		t.Fatal("clientGone = false, want true")
	}
	if w.writes != 1 {
		t.Fatalf("writes = %d, want exactly first failed write", w.writes)
	}
}

func TestWebGroupPublisherRejectsStreamFailureWithoutSSE(t *testing.T) {
	events := make(chan pkgchannel.Event, 1)
	events <- pkgchannel.Event{Err: errors.New("upstream failed")}
	close(events)
	w := &failingStreamWriter{}
	publisher := &webGroupPublisher{w: w, flusher: w}
	err := publisher.Publish(context.Background(), channel.GroupPublishRequest{
		Stream: &pkgchannel.ChatStream{Events: events},
	})
	if err == nil {
		t.Fatal("Publish unexpectedly accepted a failed replay")
	}
	if w.writes != 0 {
		t.Fatalf("writes = %d, want no SSE failure frame", w.writes)
	}
}
