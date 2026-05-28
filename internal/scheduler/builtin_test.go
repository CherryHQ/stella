package scheduler

import (
	"context"
	"testing"
)

func TestRecallyRSSBuiltinSpec(t *testing.T) {
	if RecallyRSSBuiltin.Schedule.Every != "6h" {
		t.Errorf("Schedule.Every = %q, want %q", RecallyRSSBuiltin.Schedule.Every, "6h")
	}
	if RecallyRSSBuiltin.SessionMode != SessionNew {
		t.Errorf("SessionMode = %q, want %q", RecallyRSSBuiltin.SessionMode, SessionNew)
	}
	if RecallyRSSBuiltin.ExecScope != ExecScopeAllUsers {
		t.Errorf("ExecScope = %q, want %q", RecallyRSSBuiltin.ExecScope, ExecScopeAllUsers)
	}
}

func TestRecallyDigestBuiltinSpec(t *testing.T) {
	if RecallyDigestBuiltin.Schedule.Every != "24h" {
		t.Errorf("Schedule.Every = %q, want %q", RecallyDigestBuiltin.Schedule.Every, "24h")
	}
	if RecallyDigestBuiltin.SessionMode != SessionNew {
		t.Errorf("SessionMode = %q, want %q", RecallyDigestBuiltin.SessionMode, SessionNew)
	}
	if RecallyDigestBuiltin.ExecScope != ExecScopeAllUsers {
		t.Errorf("ExecScope = %q, want %q", RecallyDigestBuiltin.ExecScope, ExecScopeAllUsers)
	}
}

func TestEnsureBuiltinJobs(t *testing.T) {
	db := testDB(t)
	svc, orgID := newServiceWithOrg(t, db)
	if err := svc.RegisterBuiltin(RecallyRSSBuiltin); err != nil {
		t.Fatalf("RegisterBuiltin(RecallyRSS): %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	svc.EnsureBuiltinJobs(orgID)

	found := false
	for _, j := range svc.ListJobs() {
		if j.Name == "recally-rss" {
			found = true
			if j.ExecScope != ExecScopeAllUsers {
				t.Errorf("recally-rss ExecScope = %q, want %q", j.ExecScope, ExecScopeAllUsers)
			}
			if j.UserID != "" {
				t.Errorf("recally-rss should have no UserID, got %q", j.UserID)
			}
		}
	}
	if !found {
		t.Error("EnsureBuiltinJobs did not create recally-rss job")
	}

	// Idempotent: second call does not duplicate.
	svc.EnsureBuiltinJobs(orgID)
	count := 0
	for _, j := range svc.ListJobs() {
		if j.Name == "recally-rss" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 recally-rss job after two EnsureBuiltinJobs calls, got %d", count)
	}
}
