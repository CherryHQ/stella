package agent

import (
	"context"
	"errors"
	"testing"
)

func TestContextMutexCanceledWaiterDoesNotConsumeLock(t *testing.T) {
	var mu contextMutex
	if err := mu.Lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mu.Lock(ctx) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Lock error = %v, want context.Canceled", err)
	}
	mu.Unlock()
	if err := mu.Lock(context.Background()); err != nil {
		t.Fatalf("lock after canceled waiter: %v", err)
	}
	mu.Unlock()
}
