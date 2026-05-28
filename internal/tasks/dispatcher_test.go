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

// newDispatcherHarness builds a harness + a dispatcher driven manually via Tick.
func newDispatcherHarness(t *testing.T, runner RunnerFunc) (*testHarness, *Dispatcher) {
	h := newHarness(t)
	d := NewDispatcher(DispatcherConfig{
		Service: h.svc,
		Queries: h.q,
		Runner:  runner,
		Resolver: func(_ context.Context, _ sqlc.AgentTask) (string, bool) {
			return h.agentID, true
		},
		NewSession: func(_ context.Context, _ sqlc.AgentTask) (string, error) {
			return "sess-" + uuid.NewString()[:8], nil
		},
		TickEvery: 0,
		MaxPerOrg: 5,
		LeaseTTL:  60 * time.Second,
	})
	return h, d
}

func TestDispatcher_PicksUpReadyTask(t *testing.T) {
	called := atomic.Int32{}
	runner := func(ctx context.Context, _ sqlc.AgentTaskRun, tool *TaskControlTool) error {
		called.Add(1)
		return tool.Submit(ctx, "{}")
	}
	h, d := newDispatcherHarness(t, runner)
	id := h.createTask(t, StatusReady)
	d.Tick(context.Background())
	d.WaitIdle()
	if called.Load() != 1 {
		t.Fatalf("runner called %d times, want 1", called.Load())
	}
	if got := h.getTask(t, id).Status; got != StatusDone {
		t.Errorf("status=%q want done", got)
	}
}

func TestDispatcher_DraftTaskNotPickedUp(t *testing.T) {
	called := atomic.Int32{}
	runner := func(_ context.Context, _ sqlc.AgentTaskRun, _ *TaskControlTool) error {
		called.Add(1)
		return nil
	}
	h, d := newDispatcherHarness(t, runner)
	h.createTask(t, StatusDraft)
	d.Tick(context.Background())
	d.WaitIdle()
	if called.Load() != 0 {
		t.Errorf("draft task should not be dispatched, runner called %d times", called.Load())
	}
}

func TestDispatcher_DepGatedTask_WaitsForUpstream(t *testing.T) {
	called := atomic.Int32{}
	runner := func(ctx context.Context, _ sqlc.AgentTaskRun, tool *TaskControlTool) error {
		called.Add(1)
		return tool.Submit(ctx, "{}")
	}
	h, d := newDispatcherHarness(t, runner)
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
	// Worker runner that "hangs" — we'll simulate stale by setting a past
	// lease_expires_at directly.
	runner := func(_ context.Context, _ sqlc.AgentTaskRun, _ *TaskControlTool) error {
		return nil // not used here
	}
	h, d := newDispatcherHarness(t, runner)
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

func TestDispatcher_DepFailurePropagatesAsBlock(t *testing.T) {
	runner := func(_ context.Context, _ sqlc.AgentTaskRun, _ *TaskControlTool) error { return nil }
	h, d := newDispatcherHarness(t, runner)
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
	runner := func(ctx context.Context, run sqlc.AgentTaskRun, tool *TaskControlTool) error {
		captured = run.ExecutorAgentID.String
		return tool.Submit(ctx, "{}")
	}
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	// Seed a settings_agent row so the FK on the hint is satisfiable. (The hint
	// table FK is ON DELETE CASCADE → settings_agent; we just need any agent id
	// to exist.)
	hintAgent := uuid.NewString()
	if _, err := h.db.Exec(`INSERT INTO settings_agent (id, org_id, name, workspace) VALUES (?, ?, 'hint-agent', '/tmp')`,
		hintAgent, h.orgID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := h.q.CreateAgentTaskDispatchHint(context.Background(), sqlc.CreateAgentTaskDispatchHintParams{
		ID:              uuid.NewString(),
		TaskID:          id,
		Kind:            RunKindWorker,
		ExecutorAgentID: hintAgent,
		CreatedAt:       time.Now().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create hint: %v", err)
	}
	d := NewDispatcher(DispatcherConfig{
		Service: h.svc, Queries: h.q, Runner: runner,
		Resolver: func(_ context.Context, _ sqlc.AgentTask) (string, bool) {
			return "resolver-agent", true // should be ignored
		},
		NewSession: func(_ context.Context, _ sqlc.AgentTask) (string, error) {
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
		TaskID: id, Kind: RunKindWorker,
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
	runner := func(_ context.Context, _ sqlc.AgentTaskRun, _ *TaskControlTool) error { return nil }
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	d := NewDispatcher(DispatcherConfig{
		Service: h.svc, Queries: h.q, Runner: runner,
		Resolver: func(_ context.Context, _ sqlc.AgentTask) (string, bool) {
			return "", false
		},
		NewSession: func(_ context.Context, _ sqlc.AgentTask) (string, error) {
			return "sess", nil
		},
	})
	d.Tick(context.Background())
	d.WaitIdle()
	// Task is unchanged (still ready); event log records protocol_error.
	if got := h.getTask(t, id).Status; got != StatusReady {
		t.Errorf("status=%q want ready (claim refused)", got)
	}
	events, _ := h.q.ListAgentTaskEvents(context.Background(), nullable(id))
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
	runner := func(ctx context.Context, _ sqlc.AgentTaskRun, tool *TaskControlTool) error {
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
		return tool.Submit(ctx, "{}")
	}
	h := newHarness(t)
	for range n {
		h.createTask(t, StatusReady)
	}
	d := NewDispatcher(DispatcherConfig{
		Service: h.svc, Queries: h.q, Runner: runner,
		Resolver: func(_ context.Context, _ sqlc.AgentTask) (string, bool) {
			return "agent", true
		},
		NewSession: func(_ context.Context, _ sqlc.AgentTask) (string, error) {
			return uuid.NewString(), nil
		},
		MaxPerOrg: cap,
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
