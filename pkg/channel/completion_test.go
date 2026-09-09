package channel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type recordingCompletion struct {
	mu      sync.Mutex
	done    chan struct{}
	outcome EgressOutcome
	acked   bool
}

func newRecordingCompletion() *recordingCompletion {
	return &recordingCompletion{done: make(chan struct{})}
}

func (c *recordingCompletion) Check(context.Context) error { return nil }

func (c *recordingCompletion) Ack(_ context.Context, outcome EgressOutcome) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.acked {
		if c.outcome != outcome {
			return errors.New("conflicting completion outcome")
		}
		return nil
	}
	c.outcome = outcome
	c.acked = true
	close(c.done)
	return nil
}

func (c *recordingCompletion) Done() <-chan struct{} { return c.done }

func TestEgressOutcomeForErrorTreatsCancellationAsUnknown(t *testing.T) {
	if got := EgressOutcomeForError(nil); got != EgressDelivered {
		t.Fatalf("nil error outcome = %q, want delivered", got)
	}
	for _, err := range []error{errors.New("send failed"), context.Canceled, context.DeadlineExceeded} {
		if got := EgressOutcomeForError(err); got != EgressUnknown {
			t.Fatalf("error %v outcome = %q, want unknown", err, got)
		}
	}
}

func TestValidateGroupReplayPreservesCompletion(t *testing.T) {
	completion := newRecordingCompletion()
	events := make(chan Event, 1)
	events <- Event{Text: "answer"}
	close(events)

	replay, err := ValidateGroupReplay(context.Background(), &ChatStream{
		Events:     events,
		Completion: completion,
	})
	if err != nil {
		t.Fatalf("ValidateGroupReplay: %v", err)
	}
	if replay == nil || replay.Completion != completion {
		t.Fatalf("replay completion = %p, want %p", replay.Completion, completion)
	}
	event, ok := <-replay.Events
	if !ok || event.Text != "answer" {
		t.Fatalf("replayed event = %#v, ok=%v", event, ok)
	}
}

func TestChatStreamCompletionDoneWaitsForAck(t *testing.T) {
	completion := newRecordingCompletion()
	stream := &ChatStream{Completion: completion}

	select {
	case <-stream.CompletionDone():
		t.Fatal("completion released before adapter acknowledgement")
	default:
	}
	if err := stream.Ack(context.Background(), EgressUnknown); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	select {
	case <-stream.CompletionDone():
	case <-time.After(time.Second):
		t.Fatal("completion did not release after acknowledgement")
	}
}
