package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/runcontrol"
)

func TestCompletionBarrierDoneCapturedBeforeBind(t *testing.T) {
	barrier := NewCompletionBarrier()
	done := barrier.Done()
	completion := &barrierTestCompletion{done: make(chan struct{})}
	if err := barrier.bind(completion); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := barrier.Ack(context.Background(), runcontrol.OutcomeDelivered); err != nil {
		t.Fatalf("ack: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Done channel captured before bind did not close after Ack")
	}
	if got := barrier.Done(); got != done {
		t.Fatal("Done returned a different channel after bind")
	}
}

type barrierTestCompletion struct {
	done chan struct{}
}

func (c *barrierTestCompletion) Check(context.Context) error { return nil }
func (c *barrierTestCompletion) Ack(context.Context, runcontrol.Outcome) error {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return nil
}
func (c *barrierTestCompletion) Done() <-chan struct{} { return c.done }
