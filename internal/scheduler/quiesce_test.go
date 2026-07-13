package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"
)

// refState reads the live registration state under the lock.
func refState(s *Service, id string) (ref schedRef, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, ok = s.refs[id]
	return
}

func quiescing(s *Service) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quiescing
}

// TestQuiesceRemovesPeriodicsPreservesOneTime pins the drain contract: recurring
// registrations are torn down so no further tick enqueues, while a durable
// one-time job already inserted into River is left to fire.
func TestQuiesceRemovesPeriodicsPreservesOneTime(t *testing.T) {
	svc := testService(t)

	recurring := addTestJob(t, svc, "recurring", "msg", Schedule{Every: "1h"}, "")
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	oneTime := addTestJob(t, svc, "onetime", "msg", Schedule{At: at}, "")

	if _, ok := refState(svc, recurring.ID); !ok {
		t.Fatal("recurring job should have a live registration before quiesce")
	}
	if ref, ok := refState(svc, oneTime.ID); !ok || !ref.isOneTime {
		t.Fatal("one-time job should have a durable one-time registration before quiesce")
	}

	svc.Quiesce()

	if !quiescing(svc) {
		t.Fatal("Quiesce did not mark the service quiescing")
	}
	if _, ok := refState(svc, recurring.ID); ok {
		t.Fatal("Quiesce did not remove the recurring periodic registration")
	}
	if ref, ok := refState(svc, oneTime.ID); !ok || !ref.isOneTime {
		t.Fatal("Quiesce removed the durable one-time registration")
	}

	// Idempotent.
	svc.Quiesce()
}

// TestQuiesceRejectsLateScheduling verifies both scheduling entry points refuse
// to enqueue new work once quiescing.
func TestQuiesceRejectsLateScheduling(t *testing.T) {
	svc := testService(t)

	existing := addTestJob(t, svc, "existing", "msg", Schedule{Every: "1h"}, "")

	svc.Quiesce()

	_, err := svc.AddJobWithOwner("late", "msg", Schedule{Every: "1h"}, "", "", "")
	if !errors.Is(err, ErrSchedulerQuiescing) {
		t.Fatalf("AddJobWithOwner after quiesce = %v, want ErrSchedulerQuiescing", err)
	}

	_, err = svc.RunJobNow(context.Background(), existing.ID)
	if !errors.Is(err, ErrSchedulerQuiescing) {
		t.Fatalf("RunJobNow after quiesce = %v, want ErrSchedulerQuiescing", err)
	}
}

// TestQuiesceExternalRiverKeepsSharedClient verifies the external-river drain
// path: Quiesce removes periodics against the injected shared client and rejects
// new scheduling, while the shared client's lifecycle stays with its owner —
// Service.Stop must not stop it (ownsRiver is false).
func TestQuiesceExternalRiverKeepsSharedClient(t *testing.T) {
	db := testDB(t)
	svc, err := New(db, WithExternalRiver())
	if err != nil {
		t.Fatalf("New external: %v", err)
	}

	client, err := newSchedulerRiverClient(svc, db)
	if err != nil {
		t.Fatalf("build shared client: %v", err)
	}
	if err := svc.BindRiverClient(client); err != nil {
		t.Fatalf("BindRiverClient: %v", err)
	}

	ctx := context.Background()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("client start: %v", err)
	}
	clientStopped := false
	t.Cleanup(func() {
		if !clientStopped {
			_ = client.Stop(ctx)
		}
	})

	if err := svc.Start(ctx); err != nil {
		t.Fatalf("svc start: %v", err)
	}
	if svc.ownsRiver {
		t.Fatal("external-river Service must not own the shared client")
	}

	recurring := addTestJob(t, svc, "recurring", "msg", Schedule{Every: "1h"}, "")
	if _, ok := refState(svc, recurring.ID); !ok {
		t.Fatal("recurring job should be registered before quiesce")
	}

	svc.Quiesce()
	if _, ok := refState(svc, recurring.ID); ok {
		t.Fatal("Quiesce did not remove the periodic against the shared client")
	}

	// Final teardown is a no-op for the shared client in external mode.
	if err := svc.Stop(); err != nil {
		t.Fatalf("svc stop: %v", err)
	}

	// The shared client is still alive and owned by the test: it can still insert.
	if _, err := client.Insert(ctx, schedJobArgs{JobID: "probe"}, &river.InsertOpts{
		Queue:       schedulerQueue,
		MaxAttempts: 1,
	}); err != nil {
		t.Fatalf("shared client should remain usable after Service.Stop: %v", err)
	}

	if err := client.Stop(ctx); err != nil {
		t.Fatalf("client stop: %v", err)
	}
	clientStopped = true
}
