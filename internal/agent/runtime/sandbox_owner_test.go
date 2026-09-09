package runtime

import (
	"context"
	"sync"
	"testing"
	"time"
)

type recordingSandboxOwnerCloser struct {
	mu  sync.Mutex
	ids []string
}

func (r *recordingSandboxOwnerCloser) CloseSessionOwner(_ context.Context, sessionID string) error {
	r.mu.Lock()
	r.ids = append(r.ids, sessionID)
	r.mu.Unlock()
	return nil
}

func (r *recordingSandboxOwnerCloser) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func TestRuntimeCloseSessionClosesOwnerAfterIdleReap(t *testing.T) {
	owner := &recordingSandboxOwnerCloser{}
	runner := newFakeRunner()
	rt, err := New(Config{
		NewRunner:          func(context.Context, RunnerParams) (Runner, error) { return runner, nil },
		Memory:             fakeMemory{},
		SandboxOwnerCloser: owner,
		IdleTimeout:        time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	info := validInfo("retained-owner")
	if _, _, err := rt.cache.getOrCreate(context.Background(), info, "", ""); err != nil {
		t.Fatalf("get runner: %v", err)
	}
	runner.lastAct = time.Now().Add(-time.Hour)
	rt.cache.reap()
	if err := rt.CloseSession(context.Background(), info.ID); err != nil {
		t.Fatalf("close session: %v", err)
	}
	if got := owner.snapshot(); len(got) != 1 || got[0] != info.ID {
		t.Fatalf("owner close IDs = %v, want [%s]", got, info.ID)
	}
}

func TestRuntimeResetDoesNotCloseRetainedOwner(t *testing.T) {
	owner := &recordingSandboxOwnerCloser{}
	rt, err := New(Config{
		NewRunner:          func(context.Context, RunnerParams) (Runner, error) { return newFakeRunner(), nil },
		Memory:             fakeMemory{},
		SandboxOwnerCloser: owner,
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	if _, _, err := rt.cache.getOrCreate(context.Background(), validInfo("refresh-owner"), "", ""); err != nil {
		t.Fatalf("get runner: %v", err)
	}
	if err := rt.ResetRunners(); err != nil {
		t.Fatalf("reset runners: %v", err)
	}
	if got := owner.snapshot(); len(got) != 0 {
		t.Fatalf("refresh closed retained owners = %v", got)
	}
}

func TestRuntimeCloseClosesReapedOwners(t *testing.T) {
	owner := &recordingSandboxOwnerCloser{}
	rt, err := New(Config{
		NewRunner:          func(context.Context, RunnerParams) (Runner, error) { return newFakeRunner(), nil },
		Memory:             fakeMemory{},
		SandboxOwnerCloser: owner,
		IdleTimeout:        time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	for _, id := range []string{"shutdown-owner-a", "shutdown-owner-b"} {
		if _, _, err := rt.cache.getOrCreate(context.Background(), validInfo(id), "", ""); err != nil {
			t.Fatalf("get runner %s: %v", id, err)
		}
	}
	rt.cache.mu.Lock()
	for _, cs := range rt.cache.sessions {
		if r, ok := cs.r.(*fakeRunner); ok {
			r.lastAct = time.Now().Add(-time.Hour)
		}
	}
	rt.cache.mu.Unlock()
	rt.cache.reap()
	if err := rt.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	got := owner.snapshot()
	if len(got) != 2 {
		t.Fatalf("shutdown owner close IDs = %v, want two sessions", got)
	}
}
