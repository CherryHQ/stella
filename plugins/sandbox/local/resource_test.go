package local

import (
	"context"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestLocalSessionObserveResourceRequiresClosedProof(t *testing.T) {
	s := &localSession{done: make(chan struct{})}
	if got, err := s.ObserveResource(context.Background()); err != nil || got.State != sandboxpkg.ResourceStatePresent {
		t.Fatalf("open session observation = %+v, %v; want present", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ObserveResource(context.Background()); err != nil || got.State != sandboxpkg.ResourceStateAbsent {
		t.Fatalf("reaped session observation = %+v, %v; want absent", got, err)
	}
}

func TestLocalSessionObserveResourceDoesNotInferFromCloseSuccess(t *testing.T) {
	s := &localSession{done: make(chan struct{}), everNativeStarted: true}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ObserveResource(context.Background()); err != nil || got.State != sandboxpkg.ResourceStateUnknown {
		t.Fatalf("native session observation = %+v, %v; want unknown", got, err)
	}
}

func TestLocalSessionObserveResourceRejectsTrackedOwners(t *testing.T) {
	s := &localSession{done: make(chan struct{}), closed: true, procs: []*localProcess{{}}}
	if got, err := s.ObserveResource(context.Background()); err != nil || got.State != sandboxpkg.ResourceStateUnknown {
		t.Fatalf("tracked session observation = %+v, %v; want unknown", got, err)
	}
}
