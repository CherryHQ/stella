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
		forceClose: func() { order = append(order, "forceClose") },
		cancelWork: func() { order = append(order, "cancelWork") },
	}
	return d, &order
}

// TestDrainSequenceOrder asserts the spec ordering: mark unready (beginDrain) ->
// stop non-HTTP ingress -> drain accepted HTTP work -> cancel the work context
// last (which then reverse-closes subsystems via the LIFO defers).
func TestDrainSequenceOrder(t *testing.T) {
	d, order := newRecordingDrain(nil)
	d.run()

	want := []string{"beginDrain", "stopIngress", "shutdown", "cancelWork"}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("drain order = %v, want %v", *order, want)
	}
}

func TestDrainSequenceForceCloseOnBudgetExceeded(t *testing.T) {
	d, order := newRecordingDrain(errors.New("shutdown deadline exceeded"))
	d.run()

	want := []string{"beginDrain", "stopIngress", "shutdown", "forceClose", "cancelWork"}
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
