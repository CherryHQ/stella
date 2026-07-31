package webhook

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

const admitTestOwner = "00000000-0000-4000-8000-000000000001"

func issueForAdmit(t *testing.T, svc *Service) (*memStore, Candidate) {
	t.Helper()
	store := svc.store.(*memStore)
	store.binding = ChannelBinding{Type: "webhook", OwnerUserID: admitTestOwner, AgentID: "a", AgentEnabled: true}
	store.active = true
	owner := admitTestOwner
	issued, err := svc.Issue(context.Background(), IssueRequest{ChannelID: "c", CallerUserID: owner, Provider: ProviderGeneric})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cand, err := svc.ResolveCandidate(context.Background(), issued.Capability)
	if err != nil {
		t.Fatalf("ResolveCandidate: %v", err)
	}
	return store, cand
}

// TestAdmitAfterRotateInvokesNoCallback pins the deterministic lifecycle race:
// a candidate resolved before a rotate/revoke that then commits must never reach
// the admission callback.
func TestAdmitAfterRotateInvokesNoCallback(t *testing.T) {
	svc := newTestService(t, &memStore{})
	_, cand := issueForAdmit(t, svc)

	// Rotate (a lifecycle mutation) completes while the candidate is held.
	current, _ := svc.GetByChannel(context.Background(), "c", admitTestOwner)
	if _, err := svc.Rotate(context.Background(), "c", admitTestOwner, current.ETag()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	called := false
	err := svc.Admit(context.Background(), cand, func(context.Context, AdmittedInvocation) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrNotFound) || called {
		t.Fatalf("admit after rotate: err=%v called=%v, want ErrNotFound and no callback", err, called)
	}
}

func TestAdmitAfterRevokeInvokesNoCallback(t *testing.T) {
	svc := newTestService(t, &memStore{})
	_, cand := issueForAdmit(t, svc)

	if _, err := svc.Delete(context.Background(), "c", admitTestOwner); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	called := false
	err := svc.Admit(context.Background(), cand, func(context.Context, AdmittedInvocation) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrNotFound) || called {
		t.Fatalf("admit after revoke: err=%v called=%v, want ErrNotFound and no callback", err, called)
	}
}

// TestAdmitFailsClosedWhenPermissionWithdrawn proves the owner→Agent permission
// is rechecked at every admission through the injected PEP: a withdrawn
// permission denies admission (no callback), and restoring it permits the same
// endpoint again.
func TestAdmitFailsClosedWhenPermissionWithdrawn(t *testing.T) {
	allowed := &atomic.Bool{}
	allowed.Store(true)
	svc, err := NewService(Config{Store: &memStore{}, Users: testUsers{true}, Access: toggleAccess{allowed}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	_, cand := issueForAdmit(t, svc)

	// Withdraw permission: admission fails closed and starts nothing.
	allowed.Store(false)
	called := false
	if err := svc.Admit(context.Background(), cand, func(context.Context, AdmittedInvocation) error { called = true; return nil }); !errors.Is(err, ErrOwnerAgentForbidden) || called {
		t.Fatalf("withdrawn admit: err=%v called=%v, want ErrOwnerAgentForbidden and no callback", err, called)
	}

	// Restore permission: the same endpoint admits again.
	allowed.Store(true)
	admitted := false
	if err := svc.Admit(context.Background(), cand, func(context.Context, AdmittedInvocation) error { admitted = true; return nil }); err != nil || !admitted {
		t.Fatalf("restored admit: err=%v admitted=%v, want nil and callback", err, admitted)
	}
}

// TestAdmitParallelUnderRLockAndMutationWaitsThroughCallback proves concurrent
// admissions run in parallel under the read lock, and a lifecycle mutation waits
// only until the admission callbacks return (i.e. through ChatAdmitted), not
// through Agent completion.
func TestAdmitParallelUnderRLockAndMutationWaitsThroughCallback(t *testing.T) {
	svc := newTestService(t, &memStore{})
	_, cand := issueForAdmit(t, svc)

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	admit := func() {
		_ = svc.Admit(context.Background(), cand, func(context.Context, AdmittedInvocation) error {
			entered <- struct{}{}
			<-release
			return nil
		})
	}
	go admit()
	go admit()

	// Both callbacks must be able to enter simultaneously; if the read lock
	// serialized them, the second would never enter while the first blocks.
	for range 2 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("admissions did not run in parallel under RLock")
		}
	}

	// A lifecycle mutation must wait while admissions hold the read lock.
	deleteDone := make(chan struct{})
	go func() {
		_, _ = svc.Delete(context.Background(), "c", admitTestOwner)
		close(deleteDone)
	}()
	select {
	case <-deleteDone:
		t.Fatal("Delete proceeded while admissions held the read lock")
	case <-time.After(50 * time.Millisecond):
	}

	// Releasing the callbacks (admission complete) lets the mutation proceed.
	close(release)
	select {
	case <-deleteDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Delete did not proceed after admissions released the read lock")
	}
}

type toggleAccess struct{ allowed *atomic.Bool }

func (a toggleAccess) CanUseOwner(context.Context, string, string) (bool, error) {
	return a.allowed.Load(), nil
}
