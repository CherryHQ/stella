package tasks

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// submitExec is an Executor that always submits an empty output. Most
// dispatcher tests only care that the run reaches a terminal state.
func submitExec() Executor {
	return executorFunc(func(_ context.Context, _ Request) (Result, error) {
		return Result{Action: TerminalSubmit, Output: map[string]any{}}, nil
	})
}

// newDispatcherHarness builds a harness + a dispatcher driven manually via Tick.
func newDispatcherHarness(t *testing.T, exec Executor) (*testHarness, *Dispatcher) {
	h := newHarness(t)
	d := NewDispatcher(DispatcherConfig{
		Service:  h.svc,
		Queries:  h.q,
		Executor: exec,
		Resolver: func(_ context.Context, _ sqlc.AgentTask) (string, bool) {
			return h.agentID, true
		},
		NewSession: func(_ context.Context, _ sqlc.AgentTask, _ string) (string, error) {
			return "sess-" + uuid.NewString()[:8], nil
		},
		TickEvery:  0,
		MaxWorkers: 5,
		LeaseTTL:   60 * time.Second,
	})
	return h, d
}

func TestDispatcher_PicksUpReadyTask(t *testing.T) {
	called := atomic.Int32{}
	exec := executorFunc(func(_ context.Context, _ Request) (Result, error) {
		called.Add(1)
		return Result{Action: TerminalSubmit, Output: map[string]any{}}, nil
	})
	h, d := newDispatcherHarness(t, exec)
	id := h.createTask(t, StatusReady)
	d.Tick(context.Background())
	d.WaitIdle()
	if called.Load() != 1 {
		t.Fatalf("executor called %d times, want 1", called.Load())
	}
	if got := h.getTask(t, id).Status; got != StatusDone {
		t.Errorf("status=%q want done", got)
	}
}

func TestDispatcher_DraftTaskNotPickedUp(t *testing.T) {
	called := atomic.Int32{}
	exec := executorFunc(func(_ context.Context, _ Request) (Result, error) {
		called.Add(1)
		return Result{Action: TerminalSubmit, Output: map[string]any{}}, nil
	})
	h, d := newDispatcherHarness(t, exec)
	h.createTask(t, StatusDraft)
	d.Tick(context.Background())
	d.WaitIdle()
	if called.Load() != 0 {
		t.Errorf("draft task should not be dispatched, executor called %d times", called.Load())
	}
}

func TestDispatcher_DepGatedTask_WaitsForUpstream(t *testing.T) {
	called := atomic.Int32{}
	exec := executorFunc(func(_ context.Context, _ Request) (Result, error) {
		called.Add(1)
		return Result{Action: TerminalSubmit, Output: map[string]any{}}, nil
	})
	h, d := newDispatcherHarness(t, exec)
	upstream := h.createTask(t, StatusReady)
	downstream := h.createTask(t, StatusReady)
	if err := h.svc.AddDep(context.Background(), downstream, upstream, DepKindHard, OnFailureBlock); err != nil {
		t.Fatalf("AddDep: %v", err)
	}
	// First tick: only upstream should dispatch.
	d.Tick(context.Background())
	d.WaitIdle()
	if called.Load() != 1 {
		t.Errorf("only upstream should have run; got %d calls", called.Load())
	}
	if got := h.getTask(t, upstream).Status; got != StatusDone {
		t.Errorf("upstream status=%q want done", got)
	}
	// Second tick: downstream is now dispatchable.
	d.Tick(context.Background())
	d.WaitIdle()
	if got := h.getTask(t, downstream).Status; got != StatusDone {
		t.Errorf("downstream status=%q want done", got)
	}
}

func TestDispatcher_StaleRunGetsInterrupted(t *testing.T) {
	// Use a non-completing executor: interruptStaleRuns returns the task to
	// ready, and a same-tick re-dispatch must not complete it, so the assertion
	// observes the lease-expiry outcome rather than a fresh submit.
	noTerminal := executorFunc(func(_ context.Context, _ Request) (Result, error) {
		return Result{Action: TerminalNone}, nil
	})
	h, d := newDispatcherHarness(t, noTerminal)
	id := h.createTask(t, StatusReady)
	res, err := h.svc.Claim(context.Background(), ClaimParams{
		TaskID: id, NewSessionID: "sess", LeaseDuration: time.Second,
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// Force lease into the past.
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	_, err = h.db.Exec(`UPDATE agent_task_run SET lease_expires_at = ?, status = 'running' WHERE id = ?`, past, res.RunID)
	if err != nil {
		t.Fatalf("force stale: %v", err)
	}
	// Tick should interrupt the stale run.
	d.Tick(context.Background())
	d.WaitIdle()
	run, _ := h.q.GetAgentTaskRun(context.Background(), res.RunID)
	if run.Status != RunInterrupted {
		t.Errorf("run status=%q want interrupted", run.Status)
	}
	task := h.getTask(t, id)
	if task.Status != StatusReady {
		t.Errorf("task status=%q want ready after lease expiry", task.Status)
	}
}

// TestDispatcher_RetryReusesSessionNoOrphan proves a retried task reuses its
// persisted session instead of minting a fresh one each dispatch — the
// orphan-session half of CR-002.
func TestDispatcher_RetryReusesSessionNoOrphan(t *testing.T) {
	var mintCalls, attempts atomic.Int32
	exec := executorFunc(func(_ context.Context, _ Request) (Result, error) {
		if attempts.Add(1) == 1 {
			return Result{Action: TerminalFail, Failure: &FailureResult{Reason: "transient", Retryable: true}}, nil
		}
		return Result{Action: TerminalSubmit, Output: map[string]any{}}, nil
	})
	h := newHarness(t)
	d := NewDispatcher(DispatcherConfig{
		Service:  h.svc,
		Queries:  h.q,
		Executor: exec,
		Resolver: func(_ context.Context, _ sqlc.AgentTask) (string, bool) { return h.agentID, true },
		NewSession: func(_ context.Context, _ sqlc.AgentTask, _ string) (string, error) {
			mintCalls.Add(1)
			return "sess-fixed", nil
		},
		MaxWorkers: 5,
		LeaseTTL:   60 * time.Second,
	})
	id := h.createTask(t, StatusReady)

	d.Tick(context.Background()) // claim run 1; executor fails retryably -> ready
	d.WaitIdle()
	if got := h.getTask(t, id).Status; got != StatusReady {
		t.Fatalf("after first tick status=%q want ready", got)
	}
	d.Tick(context.Background()) // re-dispatch retry; must reuse session
	d.WaitIdle()
	if got := h.getTask(t, id).Status; got != StatusDone {
		t.Fatalf("after second tick status=%q want done", got)
	}
	if mintCalls.Load() != 1 {
		t.Errorf("NewSession called %d times, want 1 (no orphan session on retry)", mintCalls.Load())
	}
}

func TestDispatcher_DepFailurePropagatesAsBlock(t *testing.T) {
	h, d := newDispatcherHarness(t, submitExec())
	// Upstream task in failed state. Downstream depends on it hard+block.
	upstream := h.createTask(t, StatusReady)
	// Force upstream to failed.
	if err := h.svc.Cancel(context.Background(), upstream, "test", SystemActor()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	_, _ = h.db.Exec(`UPDATE agent_task SET status = 'failed' WHERE id = ?`, upstream)
	downstream := h.createTask(t, StatusReady)
	if err := h.svc.AddDep(context.Background(), downstream, upstream, DepKindHard, OnFailureBlock); err != nil {
		t.Fatalf("AddDep: %v", err)
	}
	d.Tick(context.Background())
	d.WaitIdle()
	if got := h.getTask(t, downstream).Status; got != StatusBlocked {
		t.Errorf("downstream status=%q want blocked", got)
	}
}

func TestDispatcher_DispatchHintWinsOverResolver(t *testing.T) {
	captured := ""
	exec := executorFunc(func(_ context.Context, req Request) (Result, error) {
		captured = req.Run.ExecutorAgentID.String
		return Result{Action: TerminalSubmit, Output: map[string]any{}}, nil
	})
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	// Seed a agent row so the FK on the hint is satisfiable. (The hint
	// table FK is ON DELETE CASCADE → agent; we just need any agent id
	// to exist.)
	hintAgent := uuid.NewString()
	if _, err := h.db.Exec(`INSERT INTO agent (id, name, workspace) VALUES (?, 'hint-agent', '/tmp')`,
		hintAgent); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := h.q.CreateAgentTaskDispatchHint(context.Background(), sqlc.CreateAgentTaskDispatchHintParams{
		ID:              uuid.NewString(),
		TaskID:          nullable(id),
		Kind:            RunKindWorker,
		ExecutorAgentID: hintAgent,
		CreatedAt:       time.Now().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create hint: %v", err)
	}
	d := NewDispatcher(DispatcherConfig{
		Service: h.svc, Queries: h.q, Executor: exec,
		Resolver: func(_ context.Context, _ sqlc.AgentTask) (string, bool) {
			return "resolver-agent", true // should be ignored
		},
		NewSession: func(_ context.Context, _ sqlc.AgentTask, _ string) (string, error) {
			return "sess", nil
		},
	})
	d.Tick(context.Background())
	d.WaitIdle()
	if captured != hintAgent {
		t.Errorf("executor=%q want hint=%q (resolver should not win)", captured, hintAgent)
	}
	// And the hint should now be consumed.
	hint, _ := h.q.GetLiveDispatchHintForTask(context.Background(), sqlc.GetLiveDispatchHintForTaskParams{
		TaskID: nullable(id), Kind: RunKindWorker,
	})
	if hint.ID == "" {
		// Live hint query returns sql.ErrNoRows for consumed hints; the empty
		// row above is from an err branch we ignore. Verify directly:
		row := h.db.QueryRow(`SELECT consumed_at FROM agent_task_dispatch_hint WHERE task_id = ?`, id)
		var consumed sql.NullString
		_ = row.Scan(&consumed)
		if !consumed.Valid {
			t.Errorf("hint should be consumed, got null consumed_at")
		}
	}
}

func TestDispatcher_NoExecutorResolved_EmitsProtocolError(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	d := NewDispatcher(DispatcherConfig{
		Service: h.svc, Queries: h.q, Executor: submitExec(),
		Resolver: func(_ context.Context, _ sqlc.AgentTask) (string, bool) {
			return "", false
		},
		NewSession: func(_ context.Context, _ sqlc.AgentTask, _ string) (string, error) {
			return "sess", nil
		},
	})
	d.Tick(context.Background())
	d.WaitIdle()
	// Task is unchanged (still ready); event log records protocol_error.
	if got := h.getTask(t, id).Status; got != StatusReady {
		t.Errorf("status=%q want ready (claim refused)", got)
	}
	events, _ := h.q.ListAgentTaskEvents(context.Background(), sqlc.ListAgentTaskEventsParams{TaskID: nullable(id), Limit: 1000})
	found := false
	for _, e := range events {
		if e.EventType == "protocol_error" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected protocol_error event on task %s", id)
	}
}

func TestDispatcher_ConcurrencyCapHonored(t *testing.T) {
	const cap = 2
	const n = 5
	var live atomic.Int32
	maxObserved := atomic.Int32{}
	gate := make(chan struct{})
	exec := executorFunc(func(_ context.Context, _ Request) (Result, error) {
		cur := live.Add(1)
		defer live.Add(-1)
		for {
			m := maxObserved.Load()
			if cur > m && !maxObserved.CompareAndSwap(m, cur) {
				continue
			}
			break
		}
		<-gate
		return Result{Action: TerminalSubmit, Output: map[string]any{}}, nil
	})
	h := newHarness(t)
	for range n {
		h.createTask(t, StatusReady)
	}
	d := NewDispatcher(DispatcherConfig{
		Service: h.svc, Queries: h.q, Executor: exec,
		Resolver: func(_ context.Context, _ sqlc.AgentTask) (string, bool) {
			return "agent", true
		},
		NewSession: func(_ context.Context, _ sqlc.AgentTask, _ string) (string, error) {
			return uuid.NewString(), nil
		},
		MaxWorkers: cap,
	})
	d.Tick(context.Background())
	// Wait briefly for workers to start.
	time.Sleep(50 * time.Millisecond)
	if got := live.Load(); got > int32(cap) {
		t.Errorf("live workers=%d exceeds cap=%d", got, cap)
	}
	close(gate)
	d.WaitIdle()
	if maxObserved.Load() > int32(cap) {
		t.Errorf("max observed workers=%d exceeds cap=%d", maxObserved.Load(), cap)
	}
}
