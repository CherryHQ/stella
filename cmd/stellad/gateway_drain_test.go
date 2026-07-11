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

func TestDrainSequenceOrder(t *testing.T) {
	d, order := newRecordingDrain(nil)
	d.run()

	want := []string{"beginDrain", "shutdown", "cancelWork"}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("drain order = %v, want %v", *order, want)
	}
}

func TestDrainSequenceForceCloseOnBudgetExceeded(t *testing.T) {
	d, order := newRecordingDrain(errors.New("shutdown deadline exceeded"))
	d.run()

	want := []string{"beginDrain", "shutdown", "forceClose", "cancelWork"}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("drain order = %v, want %v", *order, want)
	}
}
