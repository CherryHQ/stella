package main

// Issue #708 Section D: the composition root runs components under one errgroup.
// Expected shutdown errors normalize to nil; an unexpected component error
// cancels peers and becomes the root error.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"golang.org/x/sync/errgroup"
)

func TestNormalizeRunErr(t *testing.T) {
	if got := normalizeRunErr(nil); got != nil {
		t.Errorf("nil -> %v, want nil", got)
	}
	if got := normalizeRunErr(context.Canceled); got != nil {
		t.Errorf("context.Canceled -> %v, want nil (orchestrated shutdown)", got)
	}
	boom := errors.New("boom")
	if got := normalizeRunErr(boom); !errors.Is(got, boom) {
		t.Errorf("unexpected error -> %v, want it to propagate", got)
	}
}

func TestNormalizeServeErr(t *testing.T) {
	if got := normalizeServeErr(nil); got != nil {
		t.Errorf("nil -> %v, want nil", got)
	}
	if got := normalizeServeErr(http.ErrServerClosed); got != nil {
		t.Errorf("http.ErrServerClosed -> %v, want nil (orchestrated shutdown)", got)
	}
	boom := errors.New("listen fail")
	if got := normalizeServeErr(boom); !errors.Is(got, boom) {
		t.Errorf("unexpected error -> %v, want it wrapped and propagated", got)
	}
}

// TestErrGroupUnexpectedErrorCancelsPeersAndPropagates mirrors runServer's
// wiring: a crashing component returns an unexpected error, a peer blocked on the
// group context is cancelled, and g.Wait returns the crash as the root error
// (the peer's own context.Canceled normalizes away).
func TestErrGroupUnexpectedErrorCancelsPeersAndPropagates(t *testing.T) {
	parent := t.Context()
	g, gctx := errgroup.WithContext(parent)

	sentinel := errors.New("component boom")
	peerCancelled := make(chan struct{})

	g.Go(func() error {
		<-gctx.Done() // blocked like groupDispatcher.Run until a peer errors
		close(peerCancelled)
		return normalizeRunErr(gctx.Err())
	})
	g.Go(func() error { return sentinel })

	err := g.Wait()
	if !errors.Is(err, sentinel) {
		t.Fatalf("root error = %v, want %v", err, sentinel)
	}
	select {
	case <-peerCancelled:
	default:
		t.Fatal("peer was not cancelled by the crashing component")
	}
}

// TestErrGroupExpectedErrorsNormalizeToNil mirrors an orchestrated shutdown:
// cancelling the work context unblocks every component, which returns its
// expected shutdown error; g.Wait returns nil (clean shutdown, not a failure).
func TestErrGroupExpectedErrorsNormalizeToNil(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	g, gctx := errgroup.WithContext(parent)

	g.Go(func() error { <-gctx.Done(); return normalizeServeErr(http.ErrServerClosed) })
	g.Go(func() error { <-gctx.Done(); return normalizeRunErr(gctx.Err()) })

	cancel() // orchestrated shutdown cancels the work context (drainSequence step 4)

	if err := g.Wait(); err != nil {
		t.Fatalf("orchestrated shutdown returned %v, want nil", err)
	}
}
