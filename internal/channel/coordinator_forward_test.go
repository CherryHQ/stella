package channel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
)

// A cancelled platform turn must leave its cause on the stream, exactly like
// the web and dispatch copy layers do. Before this, chatWithRC silently
// dropped events on ctx.Done and the consumer saw an ordinary end-of-stream,
// so logs recorded "reply incomplete" instead of "cancelled".
func TestForwardAgentEventsReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan agent.Event, 4)
	out := forwardAgentEvents(ctx, events)

	events <- agent.Event{Text: "hello"}
	first := <-out
	if first.Text != "hello" || first.Err != nil {
		t.Fatalf("expected forwarded text event, got %+v", first)
	}

	cancel()
	// The forwarder only notices ctx.Done while blocked on a send, so keep
	// feeding until the Err event lands or the stream closes without one.
	go func() {
		for range 200 {
			select {
			case events <- agent.Event{Text: "tick"}:
			default:
			}
			time.Sleep(time.Millisecond)
		}
		close(events)
	}()

	var sawCancel bool
	for evt := range out {
		if errors.Is(evt.Err, context.Canceled) {
			sawCancel = true
			break
		}
	}
	if !sawCancel {
		t.Fatal("stream ended without surfacing context.Canceled")
	}
	// The upstream must be drained after cancellation so the model side never
	// blocks; the goroutine above closes events, so out must close too.
	for range out {
	}
}
