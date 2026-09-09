package none

import (
	"context"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestNoneSessionObserveResourceRequiresClosedProof(t *testing.T) {
	s := &noneSession{done: make(chan struct{})}
	if got, err := s.ObserveResource(context.Background()); err != nil || got.State != sandboxpkg.ResourceStatePresent {
		t.Fatalf("open session observation = %+v, %v; want present", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ObserveResource(context.Background()); err != nil || got.State != sandboxpkg.ResourceStateAbsent {
		t.Fatalf("never-started session observation = %+v, %v; want absent", got, err)
	}
}

func TestNoneSessionObserveResourceDoesNotInferFromCloseSuccess(t *testing.T) {
	s := &noneSession{done: make(chan struct{}), everNativeStarted: true}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ObserveResource(context.Background()); err != nil || got.State != sandboxpkg.ResourceStateUnknown {
		t.Fatalf("native session observation = %+v, %v; want unknown", got, err)
	}
}

func TestNoneSessionObserveResourceRejectsTrackedOwners(t *testing.T) {
	s := &noneSession{done: make(chan struct{}), closed: true, procs: []*noneProcess{{}}}
	if got, err := s.ObserveResource(context.Background()); err != nil || got.State != sandboxpkg.ResourceStateUnknown {
		t.Fatalf("tracked session observation = %+v, %v; want unknown", got, err)
	}
}
