package scheduler

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuiltinHandlerDispatch(t *testing.T) {
	db := testDB(t)
	svc, orgID := newServiceWithOrg(t, db)

	var handlerCalls int32
	var fallbackCalls int32

	svc.SetOnJob(func(_ context.Context, _ Job) error {
		atomic.AddInt32(&fallbackCalls, 1)
		return nil
	})

	// Handler-mode builtins must be registered BEFORE Start so persisted
	// rows loaded during Start can find their handler.
	if err := svc.RegisterBuiltin(BuiltinJob{
		Name:     "reflect-review-test",
		Schedule: Schedule{Every: "1h"},
		Handler: func(_ context.Context, _ Job) error {
			atomic.AddInt32(&handlerCalls, 1)
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}

	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	svc.EnsureBuiltinJobs(orgID)

	var jobID string
	for _, j := range svc.ListJobs() {
		if j.Name == "reflect-review-test" {
			jobID = j.ID
			break
		}
	}
	if jobID == "" {
		t.Fatal("EnsureBuiltinJobs did not create reflect-review-test")
	}

	if _, err := svc.RunJobNow(context.Background(), jobID); err != nil {
		t.Fatalf("RunJobNow: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&handlerCalls) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&handlerCalls); got != 1 {
		t.Errorf("handler call count = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&fallbackCalls); got != 0 {
		t.Errorf("default OnJob fired %d times for a handler-mode builtin; want 0", got)
	}
}

func TestBuiltinMessageHandlerXOR(t *testing.T) {
	svc, _ := testService(t)

	cases := []struct {
		name string
		job  BuiltinJob
		want string
	}{
		{
			name: "neither",
			job:  BuiltinJob{Name: "neither", Schedule: Schedule{Every: "1h"}},
			want: "exactly one of Message or Handler",
		},
		{
			name: "both",
			job: BuiltinJob{
				Name:     "both",
				Schedule: Schedule{Every: "1h"},
				Message:  "hi",
				Handler:  func(context.Context, Job) error { return nil },
			},
			want: "exactly one of Message or Handler",
		},
		{
			name: "missing-name",
			job: BuiltinJob{
				Schedule: Schedule{Every: "1h"},
				Handler:  func(context.Context, Job) error { return nil },
			},
			want: "Name is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.RegisterBuiltin(tc.job)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestRegisterBuiltinPanicsOnBadSpec(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RegisterBuiltin did not panic on a malformed spec")
		}
	}()
	RegisterBuiltin(BuiltinJob{Name: "panic-case", Schedule: Schedule{Every: "1h"}})
}

func TestServiceRegisterBuiltinRejectsAfterStart(t *testing.T) {
	svc, _ := testService(t)
	err := svc.RegisterBuiltin(BuiltinJob{
		Name:     "post-start",
		Schedule: Schedule{Every: "1h"},
		Handler:  func(context.Context, Job) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "called after Start") {
		t.Fatalf("expected post-Start rejection, got: %v", err)
	}
}

func TestServiceRegisterBuiltinRejectsDuplicate(t *testing.T) {
	db := testDB(t)
	svc, _ := newServiceWithOrg(t, db)
	spec := BuiltinJob{
		Name:     "duplicate-runtime",
		Schedule: Schedule{Every: "1h"},
		Handler:  func(context.Context, Job) error { return nil },
	}
	if err := svc.RegisterBuiltin(spec); err != nil {
		t.Fatalf("first RegisterBuiltin: %v", err)
	}
	if err := svc.RegisterBuiltin(spec); err == nil ||
		!strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate rejection, got: %v", err)
	}
}
