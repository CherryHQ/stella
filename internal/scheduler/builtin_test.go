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
	svc.EnsureBuiltinJobs()

	jobs := svc.ListJobs()
	found := false
	for _, j := range jobs {
		if j.Name == "recally-rss" {
			found = true
			break
		}
	}
	if !found {
		t.Error("EnsureBuiltinJobs did not create recally-rss job")
	}

	// Idempotent: calling again should not duplicate.
	svc.EnsureBuiltinJobs()
	count := 0
	for _, j := range svc.ListJobs() {
		if j.Name == "recally-rss" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 recally-rss job, got %d", count)
	}
}
