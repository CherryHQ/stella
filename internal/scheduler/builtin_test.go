package scheduler

import "testing"

func TestBuiltinRSSJobRegistered(t *testing.T) {
	builtinMu.Lock()
	defer builtinMu.Unlock()

	for _, j := range builtinJobs {
		if j.Name == "recally-rss" {
			if j.Schedule.Every != "1h" {
				t.Errorf("Schedule.Every = %q, want %q", j.Schedule.Every, "1h")
			}
			if j.SessionMode != SessionReuse {
				t.Errorf("SessionMode = %q, want %q", j.SessionMode, SessionReuse)
			}
			return
		}
	}
	t.Fatal("recally-rss builtin job not registered")
}

func TestEnsureBuiltinJobs(t *testing.T) {
	svc := testService(t)
	// recally-rss is PerUser — it must not appear from EnsureBuiltinJobs alone.
	svc.EnsureBuiltinJobs()
	for _, j := range svc.ListJobs() {
		if j.Name == "recally-rss" {
			t.Error("EnsureBuiltinJobs should not create PerUser job recally-rss")
		}
	}

	// EnsureUserBuiltinJobs creates it for a specific user.
	svc.EnsureUserBuiltinJobs(1, "anna")
	found := false
	for _, j := range svc.ListJobs() {
		if j.Name == "recally-rss" && j.UserID == 1 {
			found = true
		}
	}
	if !found {
		t.Error("EnsureUserBuiltinJobs did not create recally-rss job for user 1")
	}

	// Idempotent: second call for same user should not duplicate.
	svc.EnsureUserBuiltinJobs(1, "anna")
	count := 0
	for _, j := range svc.ListJobs() {
		if j.Name == "recally-rss" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 recally-rss job, got %d", count)
	}

	// Different user gets its own instance.
	svc.EnsureUserBuiltinJobs(2, "anna")
	count = 0
	for _, j := range svc.ListJobs() {
		if j.Name == "recally-rss" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 recally-rss jobs (one per user), got %d", count)
	}
}
