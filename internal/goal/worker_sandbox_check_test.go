package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

type execSession struct {
	exitCode int
	stdout   string
	err      error
	closed   bool
	execs    int
}

func (s *execSession) Policy() sandbox.Policy { return sandbox.Policy{} }
func (s *execSession) Alive() bool            { return !s.closed }
func (s *execSession) Done() <-chan struct{}  { return nil }
func (s *execSession) WorkingDir() string     { return "" }
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
