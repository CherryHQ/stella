package webhook

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func issueForAdmit(t *testing.T, svc *Service, userID string) (*memoryStore, IssueResult, Candidate) {
	t.Helper()
	store := svc.store.(*memoryStore)
	issued, err := svc.Create(context.Background(), CreateRequest{UserID: userID, Name: "admit", AgentID: "agent", Provider: ProviderGeneric, IsEnabled: true, WaitTimeoutSeconds: DefaultWaitTimeoutSeconds, MaxRunTimeoutSeconds: DefaultMaxRunTimeoutSeconds})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cand, err := svc.ResolveCandidate(context.Background(), issued.Capability)
	if err != nil {
		t.Fatalf("ResolveCandidate: %v", err)
	}
	return store, issued, cand
}

func newAdmitService(t *testing.T, access UserAgentAccess) (*Service, string) {
	t.Helper()
	svc, err := NewService(Config{Store: &memoryStore{rows: map[string]credentialRecord{}}, Users: activeUsers{}, Access: access})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, uuid.NewString()
}

// A candidate held across a rotate must not survive to the callback.
func TestAdmitAfterRotateInvokesNoCallback(t *testing.T) {
	svc, userID := newAdmitService(t, allowedAccess{})
	_, issued, cand := issueForAdmit(t, svc, userID)
	if _, err := svc.Rotate(context.Background(), userID, issued.Webhook.ID, issued.Webhook.ETag()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	called := false
	err := svc.Admit(context.Background(), cand, func(context.Context, AdmittedInvocation) error { called = true; return nil })
	if !errors.Is(err, ErrNotFound) || called {
		t.Fatalf("admit after rotate: err=%v called=%v, want opaque refusal", err, called)
	}
}

func TestAdmitAfterDeleteOrDisableInvokesNoCallback(t *testing.T) {
	for _, operation := range []string{"delete", "disable"} {
		t.Run(operation, func(t *testing.T) {
			svc, userID := newAdmitService(t, allowedAccess{})
			_, issued, cand := issueForAdmit(t, svc, userID)
			if operation == "delete" {
				if _, err := svc.Delete(context.Background(), userID, issued.Webhook.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				disabled := false
				if _, err := svc.Update(context.Background(), UpdateRequest{ID: issued.Webhook.ID, UserID: userID, IsEnabled: &disabled}); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			err := svc.Admit(context.Background(), cand, func(context.Context, AdmittedInvocation) error { called = true; return nil })
			if !errors.Is(err, ErrNotFound) || called {
				t.Fatalf("admit after %s: err=%v called=%v", operation, err, called)
			}
		})
	}
}

// PEP is checked during every admission, not just when the resource is created.
func TestAdmitFailsClosedWhenPermissionWithdrawn(t *testing.T) {
	allowed := &atomic.Bool{}
	allowed.Store(true)
	svc, userID := newAdmitService(t, toggleAccess{allowed})
	_, _, cand := issueForAdmit(t, svc, userID)
	allowed.Store(false)
	called := false
	if err := svc.Admit(context.Background(), cand, func(context.Context, AdmittedInvocation) error { called = true; return nil }); !errors.Is(err, ErrUserAgentForbidden) || called {
		t.Fatalf("withdrawn admit: err=%v called=%v", err, called)
	}
	allowed.Store(true)
	admitted := false
	if err := svc.Admit(context.Background(), cand, func(context.Context, AdmittedInvocation) error { admitted = true; return nil }); err != nil || !admitted {
		t.Fatalf("restored admit: err=%v admitted=%v", err, admitted)
	}
}

// Admissions share the read fence; a lifecycle write waits only through callback.
func TestUpdateWaitsForAdmitCallback(t *testing.T) {
	svc, userID := newAdmitService(t, allowedAccess{})
	_, issued, cand := issueForAdmit(t, svc, userID)
	entered, release := make(chan struct{}), make(chan struct{})
	admitDone := make(chan error, 1)
	go func() {
		admitDone <- svc.Admit(context.Background(), cand, func(context.Context, AdmittedInvocation) error { close(entered); <-release; return nil })
	}()
	<-entered
	name := "updated"
	updateDone := make(chan error, 1)
	go func() {
		_, err := svc.Update(context.Background(), UpdateRequest{ID: issued.Webhook.ID, UserID: userID, Name: &name})
		updateDone <- err
	}()
	select {
	case err := <-updateDone:
		t.Fatalf("Update completed during admission: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-admitDone; err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := <-updateDone; err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := svc.Get(context.Background(), userID, issued.Webhook.ID)
	if err != nil || got.Name != name {
		t.Fatalf("updated webhook=%+v err=%v", got, err)
	}
}

func TestAdmitParallelUnderRLockAndMutationWaitsThroughCallback(t *testing.T) {
	svc, userID := newAdmitService(t, allowedAccess{})
	_, issued, cand := issueForAdmit(t, svc, userID)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	admit := func() {
		_ = svc.Admit(context.Background(), cand, func(context.Context, AdmittedInvocation) error { entered <- struct{}{}; <-release; return nil })
	}
	go admit()
	go admit()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("admissions did not run in parallel")
		}
	}
	deleteDone := make(chan struct{})
	go func() { _, _ = svc.Delete(context.Background(), userID, issued.Webhook.ID); close(deleteDone) }()
	select {
	case <-deleteDone:
		t.Fatal("Delete proceeded while admissions held read fence")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-deleteDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Delete did not proceed after admissions")
	}
}

type toggleAccess struct{ allowed *atomic.Bool }

func (a toggleAccess) CanUseUser(context.Context, string, string) (bool, error) {
	return a.allowed.Load(), nil
}
