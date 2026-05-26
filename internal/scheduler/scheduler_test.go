package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ensureTestOrg(t, db)
	return db
}

func ensureTestOrg(t *testing.T, db *sql.DB) string {
	t.Helper()
	orgID, err := appdb.EnsureDefaultOrg(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureDefaultOrg: %v", err)
	}
	return orgID
}

// newServiceWithOrg wraps New and sets the defaultOrgID.
func newServiceWithOrg(t *testing.T, db *sql.DB) *Service {
	t.Helper()
	orgID := ensureTestOrg(t, db)
	svc, err := New(db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.SetDefaultOrgID(orgID)
	return svc
}

func testService(t *testing.T) *Service {
	t.Helper()
	db := testDB(t)
	svc := newServiceWithOrg(t, db)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	return svc
}

func TestAddListRemoveJob(t *testing.T) {
	svc := testService(t)

	// Add a job.
	job, err := svc.AddJob("test", "say hello", Schedule{Every: "1h"}, "")
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	if job.ID == "" {
		t.Fatal("expected non-empty job ID")
	}
	if job.Name != "test" {
		t.Errorf("Name = %q, want %q", job.Name, "test")
	}
	if !job.Enabled {
		t.Error("expected job to be enabled")
	}

	// List jobs.
	jobs := svc.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("ListJobs: got %d, want 1", len(jobs))
	}
	if jobs[0].ID != job.ID {
		t.Errorf("job ID = %q, want %q", jobs[0].ID, job.ID)
	}

	// Verify persistence in DB.
	row, err := svc.q.GetSchedulerJob(context.Background(), sqlc.GetSchedulerJobParams{ID: job.ID, OrgID: svc.defaultOrgID})
	if err != nil {
		t.Fatalf("GetSchedulerJob: %v", err)
	}
	if row.Name != "test" {
		t.Errorf("DB name = %q, want %q", row.Name, "test")
	}

	// Remove job.
	if err := svc.RemoveJob(job.ID); err != nil {
		t.Fatalf("RemoveJob: %v", err)
	}
	if jobs := svc.ListJobs(); len(jobs) != 0 {
		t.Errorf("ListJobs after remove: got %d, want 0", len(jobs))
	}
}

func TestAddJobValidation(t *testing.T) {
	svc := testService(t)

	pastTime := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	tests := []struct {
		name    string
		jName   string
		message string
		sched   Schedule
	}{
		{"empty name", "", "msg", Schedule{Every: "1h"}},
		{"empty message", "test", "", Schedule{Every: "1h"}},
		{"no schedule", "test", "msg", Schedule{}},
		{"both cron and every", "test", "msg", Schedule{Cron: "* * * * *", Every: "1h"}},
		{"both cron and at", "test", "msg", Schedule{Cron: "* * * * *", At: time.Now().Add(time.Hour).Format(time.RFC3339)}},
		{"both every and at", "test", "msg", Schedule{Every: "1h", At: time.Now().Add(time.Hour).Format(time.RFC3339)}},
		{"all three set", "test", "msg", Schedule{Cron: "* * * * *", Every: "1h", At: time.Now().Add(time.Hour).Format(time.RFC3339)}},
		{"invalid duration", "test", "msg", Schedule{Every: "bogus"}},
		{"invalid at format", "test", "msg", Schedule{At: "not-a-timestamp"}},
		{"past at timestamp", "test", "msg", Schedule{At: pastTime}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.AddJob(tt.jName, tt.message, tt.sched, "")
			if err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestRemoveJobNotFound(t *testing.T) {
	svc := testService(t)

	if err := svc.RemoveJob("nonexistent"); err == nil {
		t.Error("expected error for nonexistent job")
	}
}

func TestJobPersistenceAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Create and add a job.
	db1, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	svc1 := newServiceWithOrg(t, db1)
	if err := svc1.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	job, err := svc1.AddJob("persist-test", "check weather", Schedule{Cron: "0 9 * * *"}, "")
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	_ = svc1.Stop()
	_ = db1.Close()

	// Create a new service from the same database.
	db2, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	svc2 := newServiceWithOrg(t, db2)
	if err := svc2.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		_ = svc2.Stop()
		_ = db2.Close()
	}()

	jobs := svc2.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("ListJobs after restart: got %d, want 1", len(jobs))
	}
	if jobs[0].ID != job.ID {
		t.Errorf("job ID = %q, want %q", jobs[0].ID, job.ID)
	}
	if jobs[0].Name != "persist-test" {
		t.Errorf("job Name = %q, want %q", jobs[0].Name, "persist-test")
	}
}

func TestStartEphemeralSkipsPersistedJobs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db1, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	svc1 := newServiceWithOrg(t, db1)
	if err := svc1.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err = svc1.AddJob("persist-test", "check weather", Schedule{Every: "1h"}, "")
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	_ = svc1.Stop()
	_ = db1.Close()

	db2, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	svc2 := newServiceWithOrg(t, db2)
	if err := svc2.StartEphemeral(context.Background()); err != nil {
		t.Fatalf("StartEphemeral: %v", err)
	}
	defer func() {
		_ = svc2.Stop()
		_ = db2.Close()
	}()

	if jobs := svc2.ListJobs(); len(jobs) != 0 {
		t.Fatalf("ListJobs after StartEphemeral: got %d, want 0", len(jobs))
	}
}

func TestOnJobCallbackFires(t *testing.T) {
	svc := testService(t)

	var mu sync.Mutex
	var fired []string
	svc.SetOnJob(func(_ context.Context, job Job) error {
		mu.Lock()
		fired = append(fired, job.ID)
		mu.Unlock()
		return nil
	})

	_, err := svc.AddJob("quick", "ping", Schedule{Every: "100ms"}, "")
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// Wait for the callback to fire.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(fired)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("callback did not fire within 2s")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestAddJobWithOwner(t *testing.T) {
	svc := testService(t)

	job, err := svc.AddJobWithOwner("owned-job", "do work", Schedule{Every: "1h"}, "", "agent-x", "99")
	if err != nil {
		t.Fatalf("AddJobWithOwner: %v", err)
	}
	if job.AgentID != "agent-x" {
		t.Errorf("AgentID = %q, want %q", job.AgentID, "agent-x")
	}
	if job.UserID != "99" {
		t.Errorf("UserID = %q, want 99", job.UserID)
	}
	if !job.Enabled {
		t.Error("expected job to be enabled")
	}

	// Verify it appears in the list.
	jobs := svc.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].ID != job.ID {
		t.Errorf("listed job ID = %q, want %q", jobs[0].ID, job.ID)
	}
}

func TestOneTimeJobCreation(t *testing.T) {
	svc := testService(t)

	futureTime := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	job, err := svc.AddJob("one-time-test", "do something once", Schedule{At: futureTime}, "")
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	if job.Schedule.At != futureTime {
		t.Errorf("At = %q, want %q", job.Schedule.At, futureTime)
	}

	jobs := svc.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("ListJobs: got %d, want 1", len(jobs))
	}
}

func TestOneTimeJobFiresAndAutoRemoves(t *testing.T) {
	svc := testService(t)

	var mu sync.Mutex
	var fired []string
	svc.SetOnJob(func(_ context.Context, job Job) error {
		mu.Lock()
		fired = append(fired, job.ID)
		mu.Unlock()
		return nil
	})

	// Schedule 200ms from now.
	at := time.Now().Add(200 * time.Millisecond).Format(time.RFC3339Nano)
	job, err := svc.AddJob("fire-once", "ping once", Schedule{At: at}, "")
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// Wait for the callback to fire and cleanup to happen.
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(fired)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("callback did not fire within 3s")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Verify the callback fired with the right job.
	mu.Lock()
	if fired[0] != job.ID {
		t.Errorf("fired job ID = %q, want %q", fired[0], job.ID)
	}
	mu.Unlock()

	// Wait a bit for the async cleanup goroutine.
	time.Sleep(200 * time.Millisecond)

	// Job should be auto-removed.
	jobs := svc.ListJobs()
	if len(jobs) != 0 {
		t.Errorf("ListJobs after one-time fire: got %d, want 0", len(jobs))
	}
}

func TestOneTimeJobSkippedOnRestartIfPast(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Create a service and add a one-time job in the future.
	db1, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	svc1 := newServiceWithOrg(t, db1)
	if err := svc1.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	futureTime := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	_, err = svc1.AddJob("restart-test", "do once", Schedule{At: futureTime}, "")
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	_ = svc1.Stop()

	// Manually tamper the job to have a past timestamp to simulate missed window.
	pastTime := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	_, err = db1.Exec("UPDATE sched_job SET schedule_at = ? WHERE name = ?", pastTime, "restart-test")
	if err != nil {
		t.Fatalf("update schedule_at: %v", err)
	}
	_ = db1.Close()

	// Restart: the past one-time job should be loaded but not scheduled (silently skipped).
	db2, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	svc2 := newServiceWithOrg(t, db2)
	if err := svc2.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		_ = svc2.Stop()
		_ = db2.Close()
	}()

	// Job is still in the list (persisted) but not scheduled with gocron.
	listed := svc2.ListJobs()
	if len(listed) != 1 {
		t.Fatalf("expected 1 persisted job, got %d", len(listed))
	}

	svc2.mu.Lock()
	_, hasGID := svc2.gids[listed[0].ID]
	svc2.mu.Unlock()
	if hasGID {
		t.Error("expected past one-time job to not be scheduled with gocron")
	}
}

func TestSessionModeDefault(t *testing.T) {
	svc := testService(t)

	// Empty session_mode defaults to "reuse".
	job, err := svc.AddJob("default-mode", "msg", Schedule{Every: "1h"}, "")
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	if job.SessionMode != SessionReuse {
		t.Errorf("SessionMode = %q, want %q", job.SessionMode, SessionReuse)
	}
}

func TestSessionModeReuse(t *testing.T) {
	svc := testService(t)

	job, err := svc.AddJob("reuse-mode", "msg", Schedule{Every: "1h"}, SessionReuse)
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// Reuse mode: SessionID is stable across calls.
	id1 := job.SessionID()
	id2 := job.SessionID()
	if id1 != id2 {
		t.Errorf("reuse mode: SessionID changed: %q vs %q", id1, id2)
	}
	if id1 != "scheduler:"+job.ID {
		t.Errorf("reuse mode: SessionID = %q, want %q", id1, "scheduler:"+job.ID)
	}
}

func TestSessionModeNew(t *testing.T) {
	svc := testService(t)

	job, err := svc.AddJob("new-mode", "msg", Schedule{Every: "1h"}, SessionNew)
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	if job.SessionMode != SessionNew {
		t.Errorf("SessionMode = %q, want %q", job.SessionMode, SessionNew)
	}

	// New mode: SessionID differs across calls.
	id1 := job.SessionID()
	time.Sleep(1 * time.Millisecond) // ensure different nano timestamp
	id2 := job.SessionID()
	if id1 == id2 {
		t.Error("new mode: SessionID should differ between calls")
	}
}

func TestSessionModeInvalid(t *testing.T) {
	svc := testService(t)

	_, err := svc.AddJob("bad-mode", "msg", Schedule{Every: "1h"}, "invalid")
	if err == nil {
		t.Error("expected error for invalid session_mode")
	}
}

func TestMigrateJobsFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Write a legacy jobs.json.
	jobs := []Job{
		{
			ID:          "abc12345",
			Name:        "legacy-job",
			Schedule:    Schedule{Every: "1h"},
			Message:     "do legacy things",
			SessionMode: SessionReuse,
			Enabled:     true,
			CreatedAt:   time.Now(),
		},
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	legacyDir := filepath.Join(dir, "scheduler")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "jobs.json"), data, 0o644); err != nil {
		t.Fatalf("write jobs.json: %v", err)
	}

	// Start a service with legacy data path.
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	svc := newServiceWithOrg(t, db)
	svc.SetLegacyDataPath(legacyDir)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		_ = svc.Stop()
		_ = db.Close()
	}()

	// Job should be loaded from DB.
	listed := svc.ListJobs()
	if len(listed) != 1 {
		t.Fatalf("expected 1 job, got %d", len(listed))
	}
	if listed[0].ID != "abc12345" {
		t.Errorf("job ID = %q, want %q", listed[0].ID, "abc12345")
	}

	// Legacy file should be removed.
	if _, err := os.Stat(filepath.Join(legacyDir, "jobs.json")); !os.IsNotExist(err) {
		t.Error("expected jobs.json to be removed after migration")
	}
}

func TestEnsureJobCreatesOnce(t *testing.T) {
	svc := testService(t)

	job1, err := svc.EnsureJob("rss-poll", "poll feeds", Schedule{Every: "1h"}, SessionReuse, "", ExecScopeSystem)
	if err != nil {
		t.Fatalf("first EnsureJob: %v", err)
	}

	job2, err := svc.EnsureJob("rss-poll", "poll feeds", Schedule{Every: "1h"}, SessionReuse, "", ExecScopeSystem)
	if err != nil {
		t.Fatalf("second EnsureJob: %v", err)
	}
	if job2.ID != job1.ID {
		t.Errorf("second call created a new job: got ID %q, want %q", job2.ID, job1.ID)
	}

	jobs := svc.ListJobs()
	if len(jobs) != 1 {
		t.Errorf("ListJobs: got %d, want 1", len(jobs))
	}
}

func TestEnsureJobUpdatesExisting(t *testing.T) {
	svc := testService(t)

	job1, err := svc.EnsureJob("rss-poll", "poll feeds v1", Schedule{Every: "1h"}, SessionReuse, "", ExecScopeSystem)
	if err != nil {
		t.Fatalf("first EnsureJob: %v", err)
	}

	job2, err := svc.EnsureJob("rss-poll", "poll feeds v2", Schedule{Every: "30m"}, SessionNew, "", ExecScopeSystem)
	if err != nil {
		t.Fatalf("second EnsureJob: %v", err)
	}
	if job2.ID != job1.ID {
		t.Errorf("update created new job: got ID %q, want %q", job2.ID, job1.ID)
	}
	if job2.Message != "poll feeds v2" {
		t.Errorf("Message = %q, want %q", job2.Message, "poll feeds v2")
	}
	if job2.Schedule.Every != "30m" {
		t.Errorf("Schedule.Every = %q, want %q", job2.Schedule.Every, "30m")
	}
	if job2.SessionMode != SessionNew {
		t.Errorf("SessionMode = %q, want %q", job2.SessionMode, SessionNew)
	}

	jobs := svc.ListJobs()
	if len(jobs) != 1 {
		t.Errorf("ListJobs: got %d, want 1", len(jobs))
	}
}

func TestEnsureJobPersistsAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db1, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	svc1 := newServiceWithOrg(t, db1)
	if err := svc1.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	job1, err := svc1.EnsureJob("rss-poll", "poll feeds", Schedule{Every: "1h"}, SessionReuse, "", ExecScopeSystem)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}
	_ = svc1.Stop()
	_ = db1.Close()

	db2, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	svc2 := newServiceWithOrg(t, db2)
	if err := svc2.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		_ = svc2.Stop()
		_ = db2.Close()
	}()

	job2, err := svc2.EnsureJob("rss-poll", "poll feeds", Schedule{Every: "1h"}, SessionReuse, "", ExecScopeSystem)
	if err != nil {
		t.Fatalf("EnsureJob after restart: %v", err)
	}
	if job2.ID != job1.ID {
		t.Errorf("EnsureJob after restart created new job: got ID %q, want %q", job2.ID, job1.ID)
	}

	jobs := svc2.ListJobs()
	if len(jobs) != 1 {
		t.Errorf("ListJobs: got %d, want 1", len(jobs))
	}
}

func TestRunJobNow_SingleRun(t *testing.T) {
	svc := testService(t)

	var fired []string
	var mu sync.Mutex
	svc.SetOnJob(func(_ context.Context, job Job) error {
		mu.Lock()
		fired = append(fired, job.ID)
		mu.Unlock()
		return nil
	})

	job, err := svc.AddJob("run-now-test", "hello", Schedule{Every: "24h"}, SessionReuse)
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	runID, err := svc.RunJobNow(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("RunJobNow: %v", err)
	}
	if runID == "" {
		t.Fatal("RunJobNow: expected non-empty run ID")
	}

	// Wait for the async goroutine to finish.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(fired)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 || fired[0] != job.ID {
		t.Errorf("callback fired for %v, want [%s]", fired, job.ID)
	}

	runs, err := svc.ListJobRuns(context.Background(), job.ID, 10)
	if err != nil {
		t.Fatalf("ListJobRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListJobRuns: got %d runs, want 1", len(runs))
	}
	if runs[0].Status != RunStatusSuccess {
		t.Errorf("run status = %q, want %q", runs[0].Status, RunStatusSuccess)
	}
}

func TestRunJobNow_PreventsConcurrentRun(t *testing.T) {
	svc := testService(t)

	// Slow job so we can attempt a second trigger while it's still running.
	started := make(chan struct{})
	unblock := make(chan struct{})
	svc.SetOnJob(func(_ context.Context, _ Job) error {
		close(started)
		<-unblock
		return nil
	})

	job, err := svc.AddJob("concurrent-test", "block", Schedule{Every: "24h"}, SessionReuse)
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	runID, err := svc.RunJobNow(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("first RunJobNow: %v", err)
	}
	if runID == "" {
		t.Fatal("first RunJobNow: expected non-empty run ID")
	}

	// Wait until the job has actually started (run record is in DB).
	<-started

	_, err = svc.RunJobNow(context.Background(), job.ID)
	if err == nil {
		t.Fatal("second RunJobNow: expected error, got nil")
	}

	close(unblock)
}

func TestRunJobNow_NotFound(t *testing.T) {
	svc := testService(t)
	_, err := svc.RunJobNow(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown job ID")
	}
}

func TestRunJobNow_AllUsers(t *testing.T) {
	svc := testService(t)

	var calledWith []string
	var mu sync.Mutex
	svc.SetOnJob(func(_ context.Context, job Job) error {
		mu.Lock()
		calledWith = append(calledWith, job.UserID)
		mu.Unlock()
		return nil
	})
	svc.SetListActiveUsersFunc(func(_ context.Context) ([]string, error) {
		return []string{"1", "2"}, nil
	})

	job, err := svc.EnsureJob("all-users-test", "hello", Schedule{Every: "1h"}, SessionReuse, "", ExecScopeAllUsers)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	runID, err := svc.RunJobNow(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("RunJobNow: %v", err)
	}
	if runID != "" {
		t.Errorf("RunJobNow all_users: expected empty run ID, got %q", runID)
	}

	// Wait for async fan-out.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(calledWith)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calledWith) != 2 {
		t.Fatalf("callback called %d times, want 2", len(calledWith))
	}
}

func TestExecuteJobForAllUsers_NoListFunc(t *testing.T) {
	svc := testService(t)

	var called int
	svc.SetOnJob(func(_ context.Context, _ Job) error {
		called++
		return nil
	})
	// Intentionally do NOT call SetListActiveUsersFunc.

	job, err := svc.EnsureJob("no-list-func", "hello", Schedule{Every: "1h"}, SessionReuse, "", ExecScopeAllUsers)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	// Should not panic; the missing func is logged and skipped.
	svc.executeJobForAllUsers(context.Background(), job, false)

	if called != 0 {
		t.Errorf("onJob called %d times, want 0", called)
	}
}
