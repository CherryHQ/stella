package goal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type execSession struct {
	exitCode int
	stdout   string
	err      error
	closed   bool
	execs    int
}

func (s *execSession) Policy() sandbox.Policy    { return sandbox.Policy{} }
func (s *execSession) Alive() bool               { return !s.closed }
func (s *execSession) Done() <-chan struct{}     { return nil }
func (s *execSession) WorkingDir() string        { return "" }
func (s *execSession) Files() sandbox.FileAccess { return sandbox.NopSession().Files() }
func (s *execSession) Close() error {
	s.closed = true
	return nil
}

func (s *execSession) Exec(context.Context, string, sandbox.ExecOptions) (sandbox.ExecResult, error) {
	s.execs++
	if s.err != nil {
		return sandbox.ExecResult{}, s.err
	}
	return sandbox.ExecResult{ExitCode: s.exitCode, Stdout: s.stdout}, nil
}

func (s *execSession) StartProcess(context.Context, sandbox.ProcessRequest) (sandbox.ProcessHandle, error) {
	return nil, nil
}

type closePathExecutor struct {
	sess *execSession
	mode string
}

func (e closePathExecutor) Execute(ctx context.Context, req ExecutorRequest) (ExecutorResult, error) {
	defer func() { _ = e.sess.Close() }()
	switch e.mode {
	case "cancel":
		return ExecutorResult{}, context.Canceled
	case "panic":
		panic("boom")
	}
	if req.OnSandboxSession != nil {
		if err := req.OnSandboxSession(e.sess); err != nil {
			return ExecutorResult{}, err
		}
	}
	return ExecutorResult{Submitted: true, Evidence: AttemptEvidence{Summary: "ok"}, Output: AttemptOutput{Summary: "ok", Hash: "h"}}, nil
}

type executorFunc func(context.Context, ExecutorRequest) (ExecutorResult, error)

func (f executorFunc) Execute(ctx context.Context, req ExecutorRequest) (ExecutorResult, error) {
	return f(ctx, req)
}

func deterministicContract(expectExit int) AcceptanceContract {
	return AcceptanceContract{Policy: PolicyDetThenJudgment, Items: []AcceptanceItem{{
		ID: "cmd", Kind: ItemDeterministic, Required: true, Command: "check", ExpectExit: &expectExit,
	}}}
}

func TestWorkerRunsChecksInLiveSandboxAndRecordsExitCode(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, deterministicContract(7))
	h.activate(d.ID)
	att, err := h.svc.Claim(context.Background(), d.ID, "w-1", nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	sess := &execSession{exitCode: 7, stdout: "checked"}
	h.worker.exec = closePathExecutor{sess: sess}
	h.worker.checks = NewCheckRunner(h.q, 0)
	if err := h.worker.Run(context.Background(), d.ID, att.ID, Actor{Type: ActorWorker}); err != nil {
		t.Fatalf("worker run: %v", err)
	}
	if !sess.closed || sess.execs != 1 {
		t.Fatalf("sandbox closed=%v execs=%d, want closed true and one exec", sess.closed, sess.execs)
	}
	events, err := h.q.ListAcceptanceEventByGoal(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 1 || !events[0].ExitCode.Valid || events[0].ExitCode.Int64 != 7 {
		t.Fatalf("event exit=%+v len=%d, want real exit 7", events[0].ExitCode, len(events))
	}
	if got := h.get(d.ID).Lifecycle; got != LifecycleDone {
		t.Fatalf("lifecycle=%q want accepted", got)
	}
}

func TestWorkerCheckFailureDoesNotBlockSandboxClose(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, deterministicContract(0))
	h.activate(d.ID)
	att, err := h.svc.Claim(context.Background(), d.ID, "w-1", nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	sess := &execSession{exitCode: 2}
	h.worker.exec = closePathExecutor{sess: sess}
	h.worker.checks = NewCheckRunner(h.q, 0)
	if err := h.worker.Run(context.Background(), d.ID, att.ID, Actor{Type: ActorWorker}); err != nil {
		t.Fatalf("worker run: %v", err)
	}
	if !sess.closed {
		t.Fatal("sandbox was not closed after acceptance failure")
	}
	events, err := h.q.ListAcceptanceEventByGoal(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 1 || events[0].Result != ResultFail || events[0].ExitCode.Int64 != 2 {
		t.Fatalf("event result/exit = len %d result %q exit %+v, want fail exit 2", len(events), events[0].Result, events[0].ExitCode)
	}
}

func TestWorkerExitPathsCloseSandbox(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		execErr error
	}{
		{name: "success"},
		{name: "check_error", execErr: errors.New("sandbox exec failed")},
		{name: "cancel", mode: "cancel"},
		{name: "panic", mode: "panic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			d := h.createRoot(KindLeaf, deterministicContract(0))
			h.activate(d.ID)
			att, err := h.svc.Claim(context.Background(), d.ID, "w-1", nil)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			sess := &execSession{err: tc.execErr}
			h.worker.exec = closePathExecutor{sess: sess, mode: tc.mode}
			h.worker.checks = NewCheckRunner(h.q, 0)
			err = h.worker.Run(context.Background(), d.ID, att.ID, Actor{Type: ActorWorker})
			if tc.mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel err=%v want context.Canceled", err)
			}
			if tc.mode == "panic" && err == nil {
				t.Fatal("panic path returned nil error")
			}
			if tc.mode != "cancel" && tc.mode != "panic" && err != nil {
				t.Fatalf("worker run: %v", err)
			}
			if !sess.closed {
				t.Fatal("sandbox was not closed")
			}
		})
	}
}

func TestWorkerOutcomeUnknownExecutorErrorBlocksWithoutReattempt(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(d.ID)
	att, err := h.svc.Claim(t.Context(), d.ID, "w-1", nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	calls := 0
	h.worker.exec = executorFunc(func(context.Context, ExecutorRequest) (ExecutorResult, error) {
		calls++
		return ExecutorResult{}, errors.New("provider connection closed after request write")
	})
	if err := h.worker.Run(t.Context(), d.ID, att.ID, Actor{Type: ActorWorker}); err != nil {
		t.Fatalf("worker run: %v", err)
	}
	gotAttempt, err := h.q.GetAttempt(t.Context(), att.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAttempt.Status != AttemptFailed || gotAttempt.FailureClass != FailureClassEnvironment || !strings.Contains(gotAttempt.Error, "outcome unknown") {
		t.Fatalf("attempt = status %q class %q error %q, want failed/environment outcome-unknown", gotAttempt.Status, gotAttempt.FailureClass, gotAttempt.Error)
	}
	gotGoal := h.get(d.ID)
	if gotGoal.Lifecycle != LifecycleBlocked || gotGoal.BlockReason != BlockEnvUnavailable {
		t.Fatalf("goal = lifecycle %q reason %q, want blocked/%s", gotGoal.Lifecycle, gotGoal.BlockReason, BlockEnvUnavailable)
	}
	if _, err := h.svc.Claim(t.Context(), d.ID, "w-2", nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("claim after outcome-unknown failure = %v, want ErrInvalidTransition", err)
	}
	if calls != 1 {
		t.Fatalf("executor calls = %d, want exactly one", calls)
	}
}

func TestWorkerPanicCannotWriteAfterAgentRunOwnershipLoss(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(d.ID)
	att, err := h.svc.Claim(t.Context(), d.ID, "w-1", nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	bootID := agentrun.NewBootID()
	if _, err := sqlc.New(h.db).CreateExecutorBoot(t.Context(), sqlc.CreateExecutorBootParams{ID: bootID}); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	store := agentrun.NewStore(h.db, bootID)
	lease, err := store.Acquire(t.Context(), att.SessionID, "goal-test")
	if err != nil {
		t.Fatalf("acquire AgentRun: %v", err)
	}
	h.worker.exec = executorFunc(func(context.Context, ExecutorRequest) (ExecutorResult, error) {
		if err := lease.Finish(t.Context(), agentrun.StatusCompleted, "replacement admitted"); err != nil {
			t.Fatalf("terminalize AgentRun: %v", err)
		}
		panic("stale executor")
	})

	err = h.worker.Run(lease.Context(), d.ID, att.ID, Actor{Type: ActorWorker})
	if err == nil {
		t.Fatal("panic path returned nil error")
	}
	got, err := h.q.GetAttempt(t.Context(), att.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if got.Status != AttemptRunning {
		t.Fatalf("stale panic changed attempt status to %q, want %q", got.Status, AttemptRunning)
	}
}
