package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// newRecordingDrain builds a drainSequence whose hooks append their name to a
// shared order slice, so run()'s step ordering can be asserted directly.
func newRecordingDrain(shutdownErr error) (*drainSequence, *[]string) {
	var order []string
	d := &drainSequence{
		beginDrain:  func() { order = append(order, "beginDrain") },
		stopIngress: func() { order = append(order, "stopIngress") },
		httpTimeout: time.Second,
		shutdownHTTP: func(context.Context) error {
			order = append(order, "shutdown")
			return shutdownErr
		},
		forceClose:   func() { order = append(order, "forceClose") },
		waitAccepted: func(context.Context) { order = append(order, "waitAccepted") },
		cancelWork:   func() { order = append(order, "cancelWork") },
	}
	return d, &order
}

// TestDrainSequenceOrder asserts the spec ordering: mark unready (beginDrain) ->
// stop non-HTTP ingress -> drain accepted HTTP work -> wait for accepted
// non-HTTP turns -> cancel the work context last (which then reverse-closes
// subsystems via the LIFO defers).
func TestDrainSequenceOrder(t *testing.T) {
	d, order := newRecordingDrain(nil)
	d.run()

	want := []string{"beginDrain", "stopIngress", "shutdown", "waitAccepted", "cancelWork"}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("drain order = %v, want %v", *order, want)
	}
}

func TestDrainSequenceForceCloseOnBudgetExceeded(t *testing.T) {
	d, order := newRecordingDrain(errors.New("shutdown deadline exceeded"))
	d.run()

	want := []string{"beginDrain", "stopIngress", "shutdown", "forceClose", "waitAccepted", "cancelWork"}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("drain order = %v, want %v", *order, want)
	}
}

// TestDrainSequenceIngressStopsBeforeCancel proves ingress is stopped before the
// work context is cancelled (outbound stays alive for accepted work until then).
func TestDrainSequenceIngressStopsBeforeCancel(t *testing.T) {
	d, order := newRecordingDrain(nil)
	d.run()

	ingressAt, cancelAt := -1, -1
	for i, step := range *order {
		switch step {
		case "stopIngress":
			ingressAt = i
		case "cancelWork":
			cancelAt = i
		}
	}
	if ingressAt < 0 || cancelAt < 0 || ingressAt >= cancelAt {
		t.Fatalf("ingress must stop before cancelWork; order = %v", *order)
	}
}

// TestDrainSequenceNilWaitAccepted proves the accepted-work wait is optional:
// a sequence without it still runs the remaining steps in order.
func TestDrainSequenceNilWaitAccepted(t *testing.T) {
	d, order := newRecordingDrain(nil)
	d.waitAccepted = nil
	d.run()

	want := []string{"beginDrain", "stopIngress", "shutdown", "cancelWork"}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("drain order = %v, want %v", *order, want)
	}
}

// TestDrainSequenceAbortCollapsesWait proves the second-signal abort collapses
// the shared budget: a waitAccepted blocked on its context unwinds as soon as
// the abort context is cancelled, and the sequence still reaches cancelWork.
func TestDrainSequenceAbortCollapsesWait(t *testing.T) {
	abortCtx, abort := context.WithCancel(context.Background())
	defer abort()

	d, order := newRecordingDrain(nil)
	// A generous budget so only the abort (not the timeout) can unblock the wait.
	d.httpTimeout = time.Minute
	d.abort = abortCtx
	d.waitAccepted = func(ctx context.Context) {
		*order = append(*order, "waitAccepted")
		<-ctx.Done()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.run()
	}()
	abort()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not unwind within 5s of the abort; the budget context is not collapsing")
	}
	want := []string{"beginDrain", "stopIngress", "shutdown", "waitAccepted", "cancelWork"}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("drain order = %v, want %v", *order, want)
	}
}

// TestDrainSequenceBudgetBoundsWait proves the drain can never hang on the
// accepted-work wait: a waitAccepted that only yields to its context (a stuck
// turn, or a tracker that never reaches zero) is released by the shared budget
// expiring, and the sequence still reaches cancelWork.
func TestDrainSequenceBudgetBoundsWait(t *testing.T) {
	d, order := newRecordingDrain(nil)
	d.httpTimeout = 50 * time.Millisecond
	d.waitAccepted = func(ctx context.Context) {
		*order = append(*order, "waitAccepted")
		<-ctx.Done()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.run()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not unwind after the budget expired; a stuck accepted-work wait can hang shutdown")
	}
	want := []string{"beginDrain", "stopIngress", "shutdown", "waitAccepted", "cancelWork"}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("drain order = %v, want %v", *order, want)
	}
}
