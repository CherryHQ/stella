package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

func TestTerminalRecorder_FirstWins(t *testing.T) {
	rec := &terminalRecorder{}
	if err := rec.record(Result{Action: TerminalSubmit, Output: "first"}); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if err := rec.record(Result{Action: TerminalFail}); err == nil {
		t.Fatal("second record should be rejected")
	}
	got, ok := rec.snapshot()
	if !ok || got.Action != TerminalSubmit || got.Output != "first" {
		t.Fatalf("snapshot=%+v ok=%v want submit/first", got, ok)
	}
}

// runExecutor claims a task, wires a fake pool whose runner behavior is driven
// by onRun, and returns the workerExecutor's Result. onRun receives the
// task_control tool and simulates the agent.
func runExecutor(t *testing.T, h *testHarness, onRun func(tc tools.Tool) error) (Result, error) {
	t.Helper()
	taskID, runID := claimSetup(t, h)
	if _, err := h.db.Exec(`UPDATE agent_task_run SET executor_agent_id = ? WHERE id = ?`, h.agentID, runID); err != nil {
		t.Fatalf("set executor: %v", err)
	}
	run, err := h.q.GetAgentTaskRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	task := h.getTask(t, taskID)

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
	exec := newWorkerExecutor(pools, nil, h.q, h.svc, nil)
	return exec.Execute(context.Background(), Request{Run: run, Task: &task})
}

func TestWorkerExecutor_SubmitRecorded(t *testing.T) {
	h := newHarness(t)
	res, err := runExecutor(t, h, func(tc tools.Tool) error {
		_, err := tc.Execute(context.Background(), map[string]any{
			"action": "submit",
			"output": map[string]any{"answer": "42"},
		})
		return err
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalSubmit {
		t.Fatalf("action=%q want submit", res.Action)
	}
}

func TestWorkerExecutor_BlockRecorded(t *testing.T) {
	h := newHarness(t)
	res, err := runExecutor(t, h, func(tc tools.Tool) error {
		_, err := tc.Execute(context.Background(), map[string]any{
			"action": "block", "kind": BlockerKindUserInput, "question": "approve?",
		})
		return err
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalBlock || res.Blocker == nil || res.Blocker.Question != "approve?" {
		t.Fatalf("res=%+v want block/approve?", res)
	}
}

func TestWorkerExecutor_FailRecorded(t *testing.T) {
	h := newHarness(t)
	res, err := runExecutor(t, h, func(tc tools.Tool) error {
		_, err := tc.Execute(context.Background(), map[string]any{
			"action": "fail", "reason": "nope", "retryable": true,
		})
		return err
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalFail || res.Failure == nil || !res.Failure.Retryable {
		t.Fatalf("res=%+v want fail/retryable", res)
	}
}

func TestWorkerExecutor_DoubleTerminal_OnlyFirst(t *testing.T) {
	h := newHarness(t)
	res, err := runExecutor(t, h, func(tc tools.Tool) error {
		if _, err := tc.Execute(context.Background(), map[string]any{"action": "submit", "output": map[string]any{}}); err != nil {
			return err
		}
		// Second terminal must be rejected by the recorder.
		if _, err := tc.Execute(context.Background(), map[string]any{"action": "fail", "reason": "late"}); err == nil {
			t.Error("second terminal action should be rejected")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalSubmit {
		t.Fatalf("action=%q want submit (first wins)", res.Action)
	}
}

func TestWorkerExecutor_CleanExit_NoTerminal(t *testing.T) {
	h := newHarness(t)
	res, err := runExecutor(t, h, func(_ tools.Tool) error { return nil })
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalNone {
		t.Fatalf("action=%q want none", res.Action)
	}
}

func TestWorkerExecutor_StreamErrorBeforeTerminal_RetryableFail(t *testing.T) {
	h := newHarness(t)
	res, err := runExecutor(t, h, func(_ tools.Tool) error { return errors.New("boom") })
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalFail || res.Failure == nil || !res.Failure.Retryable {
		t.Fatalf("res=%+v want retryable fail", res)
	}
}

func TestWorkerExecutor_NoExecutorAgent_NonRetryableFail(t *testing.T) {
	h := newHarness(t)
	taskID, runID := claimSetup(t, h)
	run, _ := h.q.GetAgentTaskRun(context.Background(), runID)
	task := h.getTask(t, taskID)
	pools := func(_ string) (agent.NewRunnerFunc, bool) { t.Fatal("pools should not be called"); return nil, false }
	exec := newWorkerExecutor(pools, nil, h.q, h.svc, nil)
	res, err := exec.Execute(context.Background(), Request{Run: run, Task: &task})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalFail || res.Failure == nil || res.Failure.Retryable {
		t.Fatalf("res=%+v want non-retryable fail", res)
	}
}

func TestWorkerExecutor_NoPool_NonRetryableFail(t *testing.T) {
	h := newHarness(t)
	taskID, runID := claimSetup(t, h)
	_, _ = h.db.Exec(`UPDATE agent_task_run SET executor_agent_id = ? WHERE id = ?`, h.agentID, runID)
	run, _ := h.q.GetAgentTaskRun(context.Background(), runID)
	task := h.getTask(t, taskID)
	pools := func(_ string) (agent.NewRunnerFunc, bool) { return nil, false }
	exec := newWorkerExecutor(pools, nil, h.q, h.svc, nil)
	res, err := exec.Execute(context.Background(), Request{Run: run, Task: &task})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalFail || res.Failure == nil || res.Failure.Retryable {
		t.Fatalf("res=%+v want non-retryable fail", res)
	}
}

var _ = sqlc.AgentTask{}
