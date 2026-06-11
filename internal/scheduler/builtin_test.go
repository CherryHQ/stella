package scheduler

import (
	"context"
	"testing"
)

func TestRecallyRSSTemplateSpec(t *testing.T) {
	if RecallyRSSTemplate.DefaultSchedule.Every != "6h" {
		t.Errorf("DefaultSchedule.Every = %q, want %q", RecallyRSSTemplate.DefaultSchedule.Every, "6h")
	}
	if RecallyRSSTemplate.SessionMode != SessionNew {
		t.Errorf("SessionMode = %q, want %q", RecallyRSSTemplate.SessionMode, SessionNew)
	}
	if RecallyRSSTemplate.Key != "recally-rss" {
		t.Errorf("Key = %q, want %q", RecallyRSSTemplate.Key, "recally-rss")
	}
}

func TestRecallyDigestTemplateSpec(t *testing.T) {
	if RecallyDigestTemplate.DefaultSchedule.Every != "24h" {
		t.Errorf("DefaultSchedule.Every = %q, want %q", RecallyDigestTemplate.DefaultSchedule.Every, "24h")
	}
	if RecallyDigestTemplate.SessionMode != SessionNew {
		t.Errorf("SessionMode = %q, want %q", RecallyDigestTemplate.SessionMode, SessionNew)
	}
	if RecallyDigestTemplate.Key != "recally-digest" {
		t.Errorf("Key = %q, want %q", RecallyDigestTemplate.Key, "recally-digest")
	}
}

func TestEnsureBuiltinJobs(t *testing.T) {
	db := testDB(t)
	svc := newTestService(t, db)
	if err := svc.RegisterBuiltin(BuiltinJob{
		Name:     "test-handler",
		Schedule: Schedule{Every: "1h"},
		Handler:  func(_ context.Context, _ Job) error { return nil },
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	svc.EnsureBuiltinJobs()

	found := false
	for _, j := range svc.ListJobs() {
		if j.Name == "test-handler" {
			found = true
			if j.OwnerKind != JobOwnerSystem {
				t.Errorf("test-handler OwnerKind = %q, want %q", j.OwnerKind, JobOwnerSystem)
			}
		}
	}
	if !found {
		t.Error("EnsureBuiltinJobs did not create test-handler job")
	}

	// Idempotent: second call does not duplicate.
	svc.EnsureBuiltinJobs()
	count := 0
	for _, j := range svc.ListJobs() {
		if j.Name == "test-handler" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 test-handler job after two EnsureBuiltinJobs calls, got %d", count)
	}
}

// TestEnsureBuiltinJobs_RetirementIdempotency inserts fake legacy system rows
// (recally-rss and recally-digest) and their run history, then calls
// EnsureBuiltinJobs twice. After the first call the rows must be gone; the
// second call must be a no-op (no error, still gone).
func TestEnsureBuiltinJobs_RetirementIdempotency(t *testing.T) {
	db := testDB(t)
	svc := newTestService(t, db)

	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	// Manually insert legacy system rows to simulate a pre-upgrade DB.
	legacyJobs := []Job{
		{
			ID:          "legacy01",
			OwnerKind:   JobOwnerSystem,
			ExecScope:   ExecScopeSystem,
			Name:        "recally-rss",
			Message:     "poll rss feeds",
			Schedule:    Schedule{Every: "6h"},
			SessionMode: SessionNew,
			Enabled:     true,
		},
		{
			ID:          "legacy02",
			OwnerKind:   JobOwnerSystem,
			ExecScope:   ExecScopeSystem,
			Name:        "recally-digest",
			Message:     "generate digest",
			Schedule:    Schedule{Every: "24h"},
			SessionMode: SessionNew,
			Enabled:     true,
		},
	}
	for _, j := range legacyJobs {
		if err := svc.insertJob(context.Background(), j); err != nil {
			t.Fatalf("insertJob(%q): %v", j.Name, err)
		}
		// Also inject into the in-memory map so EnsureBuiltinJobs can see them.
		svc.mu.Lock()
		svc.jobs[j.ID] = j
		svc.mu.Unlock()
	}

	// First call: rows should be retired.
	svc.EnsureBuiltinJobs()

	for _, j := range legacyJobs {
		for _, live := range svc.ListJobs() {
			if live.ID == j.ID {
				t.Errorf("legacy job %q (%s) still present after first EnsureBuiltinJobs", j.Name, j.ID)
			}
		}
	}

	// Second call: must be a no-op (no rows to retire, no error).
	svc.EnsureBuiltinJobs()

	for _, j := range legacyJobs {
		for _, live := range svc.ListJobs() {
			if live.ID == j.ID {
				t.Errorf("legacy job %q (%s) reappeared after second EnsureBuiltinJobs", j.Name, j.ID)
			}
		}
	}
}
