package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLifecycleGateCanceledWaiterDoesNotLeak(t *testing.T) {
	g := newLifecycleGate()
	if err := g.lockShared(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.lockExclusive(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("exclusive acquire = %v, want context.Canceled", err)
	}
	g.unlockShared()
	if err := g.lockExclusive(context.Background()); err != nil {
		t.Fatalf("exclusive acquire after canceled waiter: %v", err)
	}
	g.unlockExclusive()

	if err := g.lockExclusive(context.Background()); err != nil {
		t.Fatal(err)
	}
	readerCtx, cancelReader := context.WithCancel(context.Background())
	cancelReader()
	if err := g.lockShared(readerCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shared acquire = %v, want context.Canceled", err)
	}
	g.unlockExclusive()
	if err := g.lockShared(context.Background()); err != nil {
		t.Fatalf("shared acquire after canceled waiter: %v", err)
	}
	g.unlockShared()
}

func TestLifecycleGateWriterProgressBlocksLaterReaders(t *testing.T) {
	g := newLifecycleGate()
	if err := g.lockShared(context.Background()); err != nil {
		t.Fatal(err)
	}
	writerQueued := make(chan struct{})
	writerAcquired := make(chan struct{})
	releaseWriter := make(chan struct{})
	go func() {
		close(writerQueued)
		_ = g.lockExclusive(context.Background())
		close(writerAcquired)
		<-releaseWriter
		g.unlockExclusive()
	}()
	<-writerQueued
	// Give the weighted semaphore time to enqueue the writer before the reader.
	time.Sleep(10 * time.Millisecond)
	readerAcquired := make(chan struct{})
	go func() {
		_ = g.lockShared(context.Background())
		close(readerAcquired)
		g.unlockShared()
	}()
	g.unlockShared()
	select {
	case <-writerAcquired:
	case <-readerAcquired:
		t.Fatal("later reader bypassed queued lifecycle writer")
	case <-time.After(time.Second):
		t.Fatal("lifecycle writer made no progress")
	}
	close(releaseWriter)
	select {
	case <-readerAcquired:
	case <-time.After(time.Second):
		t.Fatal("reader did not proceed after lifecycle writer")
	}
}

func TestLifecycleGateRejectsReentrantPatternByContext(t *testing.T) {
	g := newLifecycleGate()
	if err := g.lockShared(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := g.lockExclusive(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shared-to-exclusive upgrade = %v, want deadline", err)
	}
	g.unlockShared()
}

func TestStandaloneServiceNilLifecycleGateIsExplicitNoop(t *testing.T) {
	svc, _, _ := newBarrierService(t)
	if svc.lifecycle != nil {
		t.Fatal("standalone test Service unexpectedly has a lifecycle gate")
	}
	if err := svc.withAdmissionBarrier(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
