package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

// turnFn simulates one agent Chat turn. It receives the task_control tool and
// an emit callback that streams assistant text; returning a non-nil error
// surfaces as a stream error event.
type turnFn func(tc tools.Tool, emit func(text string)) error

// fakeRunner runs a sequence of turns, one per Chat call, so tests can exercise
// the bounded protocol-repair second turn.
type fakeRunner struct {
	tc     tools.Tool
	turns  []turnFn
	idx    int
	closed bool
}

func (f *fakeRunner) Chat(_ context.Context, _ []ai.Message, _ agent.MessageContent) <-chan agent.Event {
	ev := make(chan agent.Event, 8)
	i := f.idx
	f.idx++
	go func() {
		defer close(ev)
		if i >= len(f.turns) {
			return
		}
		emit := func(text string) { ev <- agent.Event{Text: text} }
		if err := f.turns[i](f.tc, emit); err != nil {
			ev <- agent.Event{Err: err}
		}
	}()
	return ev
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
		TaskID: id, SessionID: "sess-test", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	return id, res.RunID
}

func TestBuildTaskPromptRequiresTerminalTaskControl(t *testing.T) {
	prompt := buildTaskPrompt(sqlc.AgentTask{
		Title:       "Do the work",
		Description: "Check everything",
	})
	for _, want := range []string{
		"MUST call task_control exactly once",
		`action="submit"`,
		`action="block"`,
		`action="fail"`,
		"Do not just answer in chat",
		"Do the work",
		"Check everything",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

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

// runExecutorTurns claims a task, wires a fake pool whose runner replays the
// given turns (one per Chat call), and returns the workerExecutor's Result.
func runExecutorTurns(t *testing.T, h *testHarness, turns ...turnFn) (Result, error) {
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
			return &fakeRunner{tc: tc, turns: turns}, nil
		}, true
	}
	exec := newWorkerExecutor(pools, nil, h.q, h.svc, nil)
	return exec.Execute(context.Background(), Request{Run: run, Task: &task})
}

// runExecutor is the single-turn convenience wrapper: the runner performs one
// Chat turn driven by onRun (assistant text is not exercised).
func runExecutor(t *testing.T, h *testHarness, onRun func(tc tools.Tool) error) (Result, error) {
	t.Helper()
	return runExecutorTurns(t, h, func(tc tools.Tool, _ func(string)) error { return onRun(tc) })
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

func TestWorkerExecutor_ProgressShallowMerges(t *testing.T) {
	h := newHarness(t)
	res, err := runExecutor(t, h, func(tc tools.Tool) error {
		if _, err := tc.Execute(context.Background(), map[string]any{
			"action": "progress", "patch": map[string]any{"phase": "step-1", "count": 1},
		}); err != nil {
			return err
		}
		if _, err := tc.Execute(context.Background(), map[string]any{
			"action": "progress", "patch": map[string]any{"phase": "step-2"},
		}); err != nil {
			return err
		}
		_, err := tc.Execute(context.Background(), map[string]any{"action": "submit", "output": map[string]any{}})
		return err
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalSubmit {
		t.Fatalf("action=%q want submit", res.Action)
	}
	// progress persists into agent_task.context: phase overwritten, count kept.
	var ctx string
	if err := h.db.QueryRow(`SELECT context FROM agent_task ORDER BY created_at DESC LIMIT 1`).Scan(&ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctx, `"phase":"step-2"`) || !strings.Contains(ctx, `"count":1`) {
		t.Errorf("context didn't merge as expected: %s", ctx)
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

// Phase 3: bounded protocol repair.

func TestWorkerExecutor_TextThenRepairSubmit(t *testing.T) {
	h := newHarness(t)
	res, err := runExecutorTurns(t, h,
		func(_ tools.Tool, emit func(string)) error { emit("Here is my answer in plain text."); return nil },
		func(tc tools.Tool, _ func(string)) error {
			_, e := tc.Execute(context.Background(), map[string]any{"action": "submit", "output": map[string]any{"answer": "42"}})
			return e
		},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalSubmit {
		t.Fatalf("action=%q want submit after repair", res.Action)
	}
}

func TestWorkerExecutor_TextThenRepairBlock(t *testing.T) {
	h := newHarness(t)
	res, err := runExecutorTurns(t, h,
		func(_ tools.Tool, emit func(string)) error { emit("I need more info."); return nil },
		func(tc tools.Tool, _ func(string)) error {
			_, e := tc.Execute(context.Background(), map[string]any{"action": "block", "kind": BlockerKindUserInput, "question": "approve?"})
			return e
		},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalBlock || res.Blocker == nil || res.Blocker.Question != "approve?" {
		t.Fatalf("res=%+v want block after repair", res)
	}
}

func TestWorkerExecutor_TextThenTextStaysProtocolMiss(t *testing.T) {
	h := newHarness(t)
	res, err := runExecutorTurns(t, h,
		func(_ tools.Tool, emit func(string)) error { emit("first text"); return nil },
		func(_ tools.Tool, emit func(string)) error { emit("still just text"); return nil },
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalNone {
		t.Fatalf("action=%q want none (repair failed)", res.Action)
	}
	if !res.RepairAttempted {
		t.Fatalf("RepairAttempted should be true after a failed repair turn")
	}
}

func TestWorkerExecutor_SilentExitSkipsRepair(t *testing.T) {
	h := newHarness(t)
	// First turn emits no text and no terminal; repair turn would submit. A
	// silent miss must NOT trigger repair, so the result stays TerminalNone.
	res, err := runExecutorTurns(t, h,
		func(_ tools.Tool, _ func(string)) error { return nil },
		func(tc tools.Tool, _ func(string)) error {
			_, e := tc.Execute(context.Background(), map[string]any{"action": "submit", "output": map[string]any{}})
			return e
		},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != TerminalNone {
		t.Fatalf("action=%q want none (silent exit, no repair)", res.Action)
	}
	if res.RepairAttempted {
		t.Fatalf("RepairAttempted should be false for a silent exit")
	}
}

var _ = sqlc.AgentTask{}
