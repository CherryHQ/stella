package scheduler

import "testing"

func TestBuiltinRSSJobRegistered(t *testing.T) {
	builtinMu.Lock()
	defer builtinMu.Unlock()

	for _, j := range builtinJobs {
		if j.Name == "recally-rss" {
			if j.Schedule.Every != "6h" {
				t.Errorf("Schedule.Every = %q, want %q", j.Schedule.Every, "6h")
			}
			if j.SessionMode != SessionNew {
				t.Errorf("SessionMode = %q, want %q", j.SessionMode, SessionNew)
			}
			if j.ExecScope != ExecScopeAllUsers {
				t.Errorf("ExecScope = %q, want %q", j.ExecScope, ExecScopeAllUsers)
			}
			return
		}
	}
	t.Fatal("recally-rss builtin job not registered")
}

func TestBuiltinDigestJobRegistered(t *testing.T) {
	builtinMu.Lock()
	defer builtinMu.Unlock()

	for _, j := range builtinJobs {
		if j.Name == "recally-digest" {
			if j.Schedule.Every != "24h" {
				t.Errorf("Schedule.Every = %q, want %q", j.Schedule.Every, "24h")
			}
			if j.SessionMode != SessionNew {
				t.Errorf("SessionMode = %q, want %q", j.SessionMode, SessionNew)
			}
			if j.ExecScope != ExecScopeAllUsers {
				t.Errorf("ExecScope = %q, want %q", j.ExecScope, ExecScopeAllUsers)
			}
			return
		}
	}
	t.Fatal("recally-digest builtin job not registered")
}

func TestEnsureBuiltinJobs(t *testing.T) {
	svc := testService(t)

	// EnsureBuiltinJobs creates one row per builtin regardless of ExecScope.
	svc.EnsureBuiltinJobs()

	found := false
	for _, j := range svc.ListJobs() {
		if j.Name == "recally-rss" {
			found = true
			if j.ExecScope != ExecScopeAllUsers {
				t.Errorf("recally-rss ExecScope = %q, want %q", j.ExecScope, ExecScopeAllUsers)
			}
			if j.UserID != 0 {
				t.Errorf("recally-rss should have no UserID, got %d", j.UserID)
			}
		}
	}
	if !found {
		t.Error("EnsureBuiltinJobs did not create recally-rss job")
	}

	// Idempotent: second call does not duplicate.
	svc.EnsureBuiltinJobs()
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
