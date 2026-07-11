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
func newRecordingDrain(delay time.Duration, shutdownErr error) (*drainSequence, *[]string) {
	var order []string
	d := &drainSequence{
		beginDrain:  func() { order = append(order, "beginDrain") },
		drainDelay:  delay,
		httpTimeout: time.Second,
		shutdownHTTP: func(context.Context) error {
			order = append(order, "shutdown")
			return shutdownErr
		},
		forceClose: func() { order = append(order, "forceClose") },
		cancelWork: func() { order = append(order, "cancelWork") },
		sleep:      func(context.Context, time.Duration) { order = append(order, "delay") },
	}
	return d, &order
}

func TestDrainSequenceOrder(t *testing.T) {
	d, order := newRecordingDrain(5*time.Second, nil)
	d.run(context.Background())

	want := []string{"beginDrain", "delay", "shutdown", "cancelWork"}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("drain order = %v, want %v", *order, want)
	}
}

func TestDrainSequenceForceCloseOnBudgetExceeded(t *testing.T) {
	d, order := newRecordingDrain(5*time.Second, errors.New("shutdown deadline exceeded"))
	d.run(context.Background())

	want := []string{"beginDrain", "delay", "shutdown", "forceClose", "cancelWork"}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("drain order = %v, want %v", *order, want)
	}
}

func TestDrainSequenceSkipsZeroDelay(t *testing.T) {
	d, order := newRecordingDrain(0, nil)
	d.run(context.Background())

	want := []string{"beginDrain", "shutdown", "cancelWork"}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("drain order = %v, want %v", *order, want)
	}
}
