package tasks

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/tools"
)

// fakeRunner emits events from a caller-provided channel.
type fakeRunner struct {
	events chan agent.Event
	closed bool
}

func (f *fakeRunner) Chat(_ context.Context, _ []ai.Message, _ agent.MessageContent) <-chan agent.Event {
	return f.events
}
func (f *fakeRunner) Alive() bool             { return !f.closed }
func (f *fakeRunner) Busy() bool              { return false }
func (f *fakeRunner) LastActivity() time.Time { return time.Now() }
func (f *fakeRunner) SystemPrompt() string    { return "" }
func (f *fakeRunner) Close() error            { f.closed = true; return nil }

// claimSetup creates a task, claims it, and returns the task id + run id.
func claimSetup(t *testing.T, h *testHarness) (string, string) {
	t.Helper()
	id := h.createTask(t, StatusReady)
	res, err := h.svc.Claim(context.Background(), ClaimParams{
		TaskID: id, NewSessionID: "sess-test", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	return id, res.RunID
}

// runAdapter invokes the adapter's RunnerFunc directly with onRun simulating
// the agent behavior. onRun receives the task_control tool and may invoke
// it; when it returns, the adapter's event channel closes.
func runAdapter(t *testing.T, h *testHarness, onRun func(tc tools.Tool) error) error {
	t.Helper()
	pools := func(_ string) (agent.NewRunnerFunc, bool) {
		return func(_ context.Context, p agent.RunnerParams) (agent.Runner, error) {
			var tc tools.Tool
			for _, et := range p.ExtraTools {
				if et.Definition().Name == "task_control" {
					tc = et
					break
				}
			}
			if tc == nil {
				t.Fatalf("task_control tool missing from ExtraTools")
			}
			ev := make(chan agent.Event, 4)
			go func() {
				defer close(ev)
				if err := onRun(tc); err != nil {
					ev <- agent.Event{Err: err}
				}
			}()
			return &fakeRunner{events: ev}, nil
		}, true
	}
	adapter := NewPoolAdapter(pools, nil, nil)
	runFn := adapter.AsRunnerFunc(h.q)

	_, runID := claimSetup(t, h)
	// Force the run row to have an executor_agent_id (Claim's default left it null
	// since we didn't pass one). The adapter requires non-null.
	if _, err := h.db.Exec(`UPDATE agent_task_run SET executor_agent_id = ? WHERE id = ?`, h.agentID, runID); err != nil {
		t.Fatalf("set executor: %v", err)
	}
	run, err := h.q.GetAgentTaskRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	tool := NewTaskControlTool(h.svc, h.q, run.TaskID.String, run.ID, SystemActor())
	return runFn(context.Background(), run, tool)
}

func TestPoolAdapter_SubmitPath(t *testing.T) {
	h := newHarness(t)
	err := runAdapter(t, h, func(tc tools.Tool) error {
		_, err := tc.Execute(context.Background(), map[string]any{
			"action": "submit",
			"output": map[string]any{"answer": "42"},
		})
		return err
	})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	// Need to find the latest task created and confirm done.
	rows, err := h.db.Query(`SELECT id, status FROM agent_task ORDER BY created_at DESC LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatal("no task row")
	}
	var id, status string
	if err := rows.Scan(&id, &status); err != nil {
		t.Fatal(err)
	}
	if status != StatusDone {
		t.Errorf("status=%q want done", status)
	}
}

func TestPoolAdapter_BlockPath(t *testing.T) {
	h := newHarness(t)
	err := runAdapter(t, h, func(tc tools.Tool) error {
		_, err := tc.Execute(context.Background(), map[string]any{
			"action":   "block",
			"kind":     BlockerKindUserInput,
			"question": "approve?",
		})
		return err
	})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	var status string
	_ = h.db.QueryRow(`SELECT status FROM agent_task ORDER BY created_at DESC LIMIT 1`).Scan(&status)
	if status != StatusBlocked {
		t.Errorf("status=%q want blocked", status)
	}
}

func TestPoolAdapter_NoExecutor_Fails(t *testing.T) {
	h := newHarness(t)
	id, runID := claimSetup(t, h)
	// Leave executor_agent_id NULL.
	pools := func(_ string) (agent.NewRunnerFunc, bool) { t.Fatal("should not call pools"); return nil, false }
	adapter := NewPoolAdapter(pools, nil, nil)
	runFn := adapter.AsRunnerFunc(h.q)
	run, _ := h.q.GetAgentTaskRun(context.Background(), runID)
	tool := NewTaskControlTool(h.svc, h.q, id, runID, SystemActor())
	if err := runFn(context.Background(), run, tool); err != nil {
		t.Fatalf("adapter: %v", err)
	}
	task := h.getTask(t, id)
	// Fail with retryable=false on no executor.
	if task.Status != StatusFailed {
		t.Errorf("status=%q want failed (no executor)", task.Status)
	}
}

func TestPoolAdapter_NoPool_Fails(t *testing.T) {
	h := newHarness(t)
	id, runID := claimSetup(t, h)
	_, _ = h.db.Exec(`UPDATE agent_task_run SET executor_agent_id = ? WHERE id = ?`, h.agentID, runID)
	pools := func(_ string) (agent.NewRunnerFunc, bool) { return nil, false }
	adapter := NewPoolAdapter(pools, nil, nil)
	runFn := adapter.AsRunnerFunc(h.q)
	run, _ := h.q.GetAgentTaskRun(context.Background(), runID)
	tool := NewTaskControlTool(h.svc, h.q, id, runID, SystemActor())
	if err := runFn(context.Background(), run, tool); err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if got := h.getTask(t, id).Status; got != StatusFailed {
		t.Errorf("status=%q want failed (no pool)", got)
	}
}

func TestPoolAdapter_ProtocolError_AgentDoesntCallTool(t *testing.T) {
	h := newHarness(t)
	// Runner closes the channel without invoking task_control.
	err := runAdapter(t, h, func(_ tools.Tool) error { return nil })
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	// Since the worker wrapping (Phase 3) isn't around the adapter in this
	// test, the protocol fallback isn't applied automatically here. But the
	// adapter itself returns nil cleanly, signaling "tool didn't fire" to
	// the worker.
	var status string
	_ = h.db.QueryRow(`SELECT status FROM agent_task ORDER BY created_at DESC LIMIT 1`).Scan(&status)
	if status != StatusRunning {
		t.Errorf("status=%q want still-running (worker layer applies fallback)", status)
	}
}

// silence the unused-import lint for sql if upstream changes import paths.
var _ = sql.ErrNoRows
