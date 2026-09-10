package goal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// These tests drive REAL Runtime admission for a goal turn, because Run ownership
// is only observable across that seam: the Goal layer never binds a lease itself,
// it releases the one the runtime admitted. A double could not prove that a
// cancelled, superseded, or reaped execution stops writing goal state, nor that
// every planning turn of a decomposition attempt is released.

// goalRunMemory is a minimal memory.Provider. It can fail Append, which is how a
// turn's transcript commit is made to fail while the model itself "succeeded".
type goalRunMemory struct {
	mu        sync.Mutex
	appends   int
	appendErr error
}

func (m *goalRunMemory) Name() string { return "goal-run-test" }

func (m *goalRunMemory) Bootstrap(context.Context, memory.Session) error { return nil }

func (m *goalRunMemory) Append(_ context.Context, _ memory.Session, _ ...ai.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.appendErr != nil {
		return m.appendErr
	}
	m.appends++
	return nil
}

func (m *goalRunMemory) Assemble(context.Context, memory.Session, int, int) ([]ai.Message, error) {
	return nil, nil
}

func (m *goalRunMemory) Stats(context.Context, memory.Session) (memory.SessionStats, error) {
	return memory.SessionStats{}, nil
}

func (m *goalRunMemory) Close() error { return nil }

func (m *goalRunMemory) appendCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.appends
}

// goalRunRunner answers every turn with one text block.
type goalRunRunner struct{ text string }

func (r *goalRunRunner) Chat(context.Context, []ai.Message, agentruntime.MessageContent) <-chan agentruntime.Event {
	out := make(chan agentruntime.Event, 2)
	out <- agentruntime.Event{Text: r.text}
	close(out)
	return out
}

func (r *goalRunRunner) Alive() bool             { return true }
func (r *goalRunRunner) Busy() bool              { return false }
func (r *goalRunRunner) LastActivity() time.Time { return time.Now().UTC() }
func (r *goalRunRunner) SystemPrompt() string    { return "" }
func (r *goalRunRunner) Close() error            { return nil }

func (r *goalRunRunner) PluginContext() agentruntime.PluginContext {
	return agentruntime.PluginContext{}
}

// goalRunFixture owns the production admission path a goal turn takes: a Store
// for the Run, a Runtime for the turn, and the record of what it admitted.
type goalRunFixture struct {
	t     *testing.T
	h     *harness
	store *agentrun.Store
	rt    *agentruntime.Runtime
	mem   *goalRunMemory
	runs  []string
}

func newGoalRunFixture(t *testing.T, h *harness) *goalRunFixture {
	t.Helper()
	store := agentrun.NewStore(h.db, agentrun.NewBootID())
	if err := store.RegisterBoot(t.Context()); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	t.Cleanup(store.Close)
	mem := &goalRunMemory{}
	rt, err := agentruntime.New(agentruntime.Config{
		Memory:    mem,
		AgentRuns: store,
		NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
			return &goalRunRunner{text: "planning"}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	return &goalRunFixture{t: t, h: h, store: store, rt: rt, mem: mem}
}

// admitRaw runs one real runtime turn on sessionID and returns its execution
// handoff plus the outcome the runtime published. A turn whose admission failed
// publishes nothing and carries no ownership.
func (f *goalRunFixture) admitRaw(ctx context.Context, sessionID string) (*agentruntime.ExecutionHandoff, agentruntime.TurnOutcome, bool) {
	f.t.Helper()
	handoff := agentruntime.NewExecutionHandoff()
	stream := f.rt.Chat(ctx, session.Info{
		ID: sessionID, UserID: f.h.userID, AgentID: f.h.agentID,
	}, "worker prompt", agentruntime.WithExecutionHandoff(handoff))
	var streamErr error
	for ev := range stream {
		if ev.Err != nil {
			streamErr = ev.Err
		}
	}
	if lease := handoff.Lease(); lease != nil {
		f.runs = append(f.runs, lease.Guard.RunID)
	}
	outcome, ok := handoff.Outcome()
	if streamErr != nil && !ok {
		return handoff, agentruntime.TurnOutcome{}, false
	}
	return handoff, outcome, ok
}

// admit requires a clean, admitted turn and returns its handoff.
func (f *goalRunFixture) admit(ctx context.Context, sessionID string) *agentruntime.ExecutionHandoff {
	f.t.Helper()
	handoff, outcome, ok := f.admitRaw(ctx, sessionID)
	if !ok {
		f.t.Fatalf("admit turn on %s: runtime published no execution outcome", sessionID)
	}
	if outcome.Status != agentrun.StatusCompleted || outcome.CommitErr != nil {
		f.t.Fatalf("admit turn on %s: status=%q commitErr=%v, want a clean turn", sessionID, outcome.Status, outcome.CommitErr)
	}
	if handoff.Lease() == nil {
		f.t.Fatalf("admit turn on %s: no Run was admitted", sessionID)
	}
	return handoff
}

// admitErr is admit for a turn that is allowed to fail: it reports the failure
// and returns whatever ownership (usually none) the turn ended up with.
func (f *goalRunFixture) admitErr(ctx context.Context, sessionID string) (*agentruntime.ExecutionHandoff, error) {
	f.t.Helper()
	handoff, outcome, ok := f.admitRaw(ctx, sessionID)
	if !ok {
		return handoff, errors.New("no execution outcome was published")
	}
	if outcome.Status != agentrun.StatusCompleted || outcome.CommitErr != nil {
		return handoff, errors.New("turn did not execute cleanly: " + outcome.Reason)
	}
	return handoff, nil
}

// seedSession inserts a durable conversation row so a session can admit a Run.
func (f *goalRunFixture) seedSession(ctx context.Context, sessionID string) string {
	f.t.Helper()
	if _, err := f.h.db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		f.t.Fatalf("seed conversation %s: %v", sessionID, err)
	}
	return sessionID
}

// expireAndReap takes ownership of an admitted Run away from its owner the way a
// lost heartbeat does: the lease expires, recovery terminalizes the Run, and the
// local renewal worker is cancelled.
func (f *goalRunFixture) expireAndReap(ctx context.Context, handoff *agentruntime.ExecutionHandoff) {
	f.t.Helper()
	lease := handoff.Lease()
	if lease == nil {
		f.t.Fatal("expireAndReap called without an admitted Run")
	}
	if _, err := f.h.db.Exec(ctx, `UPDATE agent_run SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, lease.Guard.RunID); err != nil {
		f.t.Fatalf("expire lease: %v", err)
	}
	if err := f.store.Reap(ctx); err != nil {
		f.t.Fatalf("reap expired Run: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.store.LocalRuns() != 0 {
		if time.Now().After(deadline) {
			f.t.Fatalf("local Runs after recovery = %d, want 0", f.store.LocalRuns())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *goalRunFixture) runStatus(ctx context.Context, runID string) string {
	f.t.Helper()
	row, err := f.h.q.GetAgentRun(ctx, runID)
	if err != nil {
		f.t.Fatalf("get Run %s: %v", runID, err)
	}
	return row.Status
}

func decompositionAttempts(t *testing.T, h *harness, goalID string) []sqlc.AgentGoalAttempt {
	t.Helper()
	attempts, err := h.q.ListAttemptByGoal(t.Context(), sqlc.ListAttemptByGoalParams{
		GoalID: goalID, Purpose: pgnull.Text(PurposeDecomposition),
	})
	if err != nil {
		t.Fatalf("list decomposition attempts: %v", err)
	}
	return attempts
}

// TestWorkerReleasesEveryDecompositionTurn proves that one decomposition attempt
// with a repair releases BOTH of its planning turns: the first before the repair
// turn is admitted (a Session admits at most one Run, so the second admission
// would fail otherwise) and the last after its plan committed. The bug this pins
// is a final repair Run left renewing until its lease expired.
func TestWorkerReleasesEveryDecompositionTurn(t *testing.T) {
	h := newHarness(t)
	fx := newGoalRunFixture(t, h)
	ctx := t.Context()
	root := h.createRoot(KindComposite, AcceptanceContract{})

	calls := 0
	h.exec.fn = func(req ExecutorRequest) (ExecutorResult, error) {
		calls++
		// A real Run is admitted per planning turn on the same planning session.
		handoff := fx.admit(ctx, req.Attempt.SessionID)
		switch calls {
		case 1:
			return ExecutorResult{
				Submitted: true, Evidence: AttemptEvidence{Summary: "bad"}, handoff: handoff,
				Decomposition: &DecompositionContent{Children: []ProposedChild{cmp_child("a", false)}},
			}, nil
		case 2:
			return ExecutorResult{
				Submitted: true, Evidence: AttemptEvidence{Summary: "fixed"}, handoff: handoff,
				Decomposition: &DecompositionContent{Children: []ProposedChild{cmp_child("a", true)}},
			}, nil
		default:
			t.Fatalf("unexpected planner call %d", calls)
			return ExecutorResult{}, nil
		}
	}

	att, err := h.svc.BeginAutoDecomposition(ctx, root.ID, nil)
	if err != nil {
		t.Fatalf("BeginAutoDecomposition: %v", err)
	}
	if err := h.worker.Run(ctx, root.ID, att.ID, Actor{Type: ActorWorker}); err != nil {
		t.Fatalf("worker Run: %v", err)
	}
	if calls != 2 {
		t.Fatalf("planner calls = %d, want 2", calls)
	}
	if len(fx.runs) != 2 {
		t.Fatalf("admitted Runs = %d, want one per planning turn", len(fx.runs))
	}
	for _, runID := range fx.runs {
		if status := fx.runStatus(ctx, runID); status != agentrun.StatusCompleted {
			t.Fatalf("Run %s status = %q, want completed", runID, status)
		}
	}
	// The decisive assertion: no planning turn is still renewing its Run.
	if live := fx.store.LocalRuns(); live != 0 {
		t.Fatalf("local Runs after the attempt = %d, want 0 (a repair turn was never released)", live)
	}
	if got := h.get(root.ID); !got.PlannedAt.Valid || got.Lifecycle != LifecycleActive {
		t.Fatalf("root planned=%v lifecycle=%q, want a planned active goal", got.PlannedAt.Valid, got.Lifecycle)
	}
	if attempts := decompositionAttempts(t, h, root.ID); len(attempts) != 1 || attempts[0].RepairRounds != 1 {
		t.Fatalf("decomposition attempts = %+v, want one with repair_rounds=1", attempts)
	}
}

// TestWorkerReleasesTheRepairTurnWhenItsAdmissionFails covers the other half: a
// repair turn that never got a Run must not leave the attempt renewing the one it
// already released, and the failure must be applied as a durable transition.
func TestWorkerReleasesTheRepairTurnWhenItsAdmissionFails(t *testing.T) {
	h := newHarness(t)
	fx := newGoalRunFixture(t, h)
	ctx := t.Context()
	root := h.createRoot(KindComposite, AcceptanceContract{})

	// A session with no durable conversation row cannot admit a Run: agent_run
	// references ctx_conversation. This is the deterministic, residue-free way to
	// script an admission that never happened.
	const unadmittableSession = "goal-session-without-a-conversation-row"

	calls := 0
	h.exec.fn = func(req ExecutorRequest) (ExecutorResult, error) {
		calls++
		switch calls {
		case 1:
			handoff := fx.admit(ctx, req.Attempt.SessionID)
			return ExecutorResult{
				Submitted: true, Evidence: AttemptEvidence{Summary: "bad"}, handoff: handoff,
				Decomposition: &DecompositionContent{Children: []ProposedChild{cmp_child("a", false)}},
			}, nil
		case 2:
			handoff, err := fx.admitErr(ctx, unadmittableSession)
			if err == nil {
				t.Fatal("repair admission succeeded on a session with no conversation row")
			}
			return ExecutorResult{
				Failed: true, FailReason: "repair admission failed", FailureClass: FailureClassEnvironment,
				BlockedBy: BlockEnvUnavailable, handoff: handoff,
			}, nil
		default:
			t.Fatalf("unexpected planner call %d", calls)
			return ExecutorResult{}, nil
		}
	}

	att, err := h.svc.BeginAutoDecomposition(ctx, root.ID, nil)
	if err != nil {
		t.Fatalf("BeginAutoDecomposition: %v", err)
	}
	if runErr := h.worker.Run(ctx, root.ID, att.ID, Actor{Type: ActorWorker}); runErr != nil {
		t.Fatalf("worker Run: %v (a failed repair is a recorded transition, not a worker error)", runErr)
	}
	if calls != 2 {
		t.Fatalf("planner calls = %d, want 2", calls)
	}
	if len(fx.runs) != 1 {
		t.Fatalf("admitted Runs = %d, want only the first planning turn", len(fx.runs))
	}
	if status := fx.runStatus(ctx, fx.runs[0]); status != agentrun.StatusCompleted {
		t.Fatalf("first turn Run status = %q, want completed", status)
	}
	if live := fx.store.LocalRuns(); live != 0 {
		t.Fatalf("local Runs = %d, want 0 after the repair admission failed", live)
	}
	if got := h.get(root.ID); got.PlannedAt.Valid {
		t.Fatal("a failed repair turn planned the goal")
	}
}

// TestStaleRunFenceRefusesTheGoalTransition proves that the attempt's source
// write is fenced by the execution ownership the turn holds: once recovery has
// taken the Run away, the Goal transition must not commit. Without the fence the
// write would be indistinguishable from a management write and would apply.
func TestStaleRunFenceRefusesTheGoalTransition(t *testing.T) {
	h := newHarness(t)
	fx := newGoalRunFixture(t, h)
	ctx := t.Context()
	root := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(root.ID)

	h.exec.fn = func(req ExecutorRequest) (ExecutorResult, error) {
		handoff := fx.admit(ctx, req.Attempt.SessionID)
		// The Run is terminalized behind the worker's back: a lost lease, an abort,
		// or a replacement owner already decided this execution.
		fx.expireAndReap(ctx, handoff)
		return ExecutorResult{
			Submitted: true, Evidence: AttemptEvidence{Summary: "unowned"}, handoff: handoff,
			Output: AttemptOutput{Summary: "unowned", Hash: "h-stale"},
		}, nil
	}

	att, err := h.svc.Claim(ctx, root.ID, "w-1", nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// The claim itself moves the leaf into its in-flight state; a stale execution
	// must leave that state exactly as it found it.
	before := h.get(root.ID).Lifecycle
	if before == LifecycleDone || before == LifecycleBlocked {
		t.Fatalf("precondition: claimed goal lifecycle = %q, want an in-flight goal", before)
	}
	runErr := h.worker.Run(ctx, root.ID, att.ID, Actor{Type: ActorWorker})
	if !errors.Is(runErr, agentrun.ErrLeaseLost) {
		t.Fatalf("worker Run error = %v, want the ownership conflict", runErr)
	}
	if got := h.get(root.ID); got.Lifecycle != before {
		t.Fatalf("goal lifecycle = %q after a stale execution's write, want %q (refused)", got.Lifecycle, before)
	}
	if attempts, err := h.q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: root.ID, Purpose: pgnull.Text(PurposeExecution)}); err != nil {
		t.Fatalf("list attempts: %v", err)
	} else if len(attempts) != 1 || attempts[0].Status != AttemptRunning {
		t.Fatalf("attempts = %+v, want the attempt left running for recovery", attempts)
	}
}

func TestStaleRunCannotRecordDeterministicAcceptance(t *testing.T) {
	h := newHarness(t)
	fx := newGoalRunFixture(t, h)
	root := h.createRoot(KindLeaf, deterministicContract(0))
	h.activate(root.ID)
	h.worker.checks = &lcl_checkRunner{pass: true}
	h.exec.fn = func(req ExecutorRequest) (ExecutorResult, error) {
		handoff := fx.admit(t.Context(), req.Attempt.SessionID)
		fx.expireAndReap(t.Context(), handoff)
		return ExecutorResult{
			Submitted: true, handoff: handoff,
			Evidence: AttemptEvidence{Summary: "stale check"},
			Output:   AttemptOutput{Summary: "stale", Hash: "stale"},
		}, nil
	}
	attempt, err := h.svc.Claim(t.Context(), root.ID, "w-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.worker.Run(t.Context(), root.ID, attempt.ID, Actor{Type: ActorWorker}); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale worker result = %v", err)
	}
	events, err := h.q.ListAcceptanceEventByGoal(t.Context(), root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("stale Run committed %d deterministic acceptance records", len(events))
	}
}

// TestStaleRunFenceRefusesRepairRoundBookkeeping pins the same fence for the
// decomposition attempt's first durable write, which is also a Run-derived write.
func TestStaleRunFenceRefusesRepairRoundBookkeeping(t *testing.T) {
	h := newHarness(t)
	fx := newGoalRunFixture(t, h)
	ctx := t.Context()
	root := h.createRoot(KindComposite, AcceptanceContract{})

	h.exec.fn = func(req ExecutorRequest) (ExecutorResult, error) {
		handoff := fx.admit(ctx, req.Attempt.SessionID)
		fx.expireAndReap(ctx, handoff)
		return ExecutorResult{
			Submitted: true, Evidence: AttemptEvidence{Summary: "unowned"}, handoff: handoff,
			Decomposition: &DecompositionContent{Children: []ProposedChild{cmp_child("a", true)}},
		}, nil
	}

	att, err := h.svc.BeginAutoDecomposition(ctx, root.ID, nil)
	if err != nil {
		t.Fatalf("BeginAutoDecomposition: %v", err)
	}
	runErr := h.worker.Run(ctx, root.ID, att.ID, Actor{Type: ActorWorker})
	if !errors.Is(runErr, agentrun.ErrLeaseLost) {
		t.Fatalf("worker Run error = %v, want the ownership conflict", runErr)
	}
	if attempts := decompositionAttempts(t, h, root.ID); len(attempts) != 1 || attempts[0].RepairRounds != 0 {
		t.Fatalf("attempts = %+v, want repair rounds untouched by stale execution", attempts)
	}
	if got := h.get(root.ID); got.PlannedAt.Valid {
		t.Fatal("a stale execution planned the goal")
	}
}

// TestRunStatusFollowsTheRuntimeOutcome pins that a committed goal transition is
// not enough on its own: when the turn's own transcript never reached durable
// storage, the Run must record an outcome that cannot be replayed, not a success.
func TestRunStatusFollowsTheRuntimeOutcome(t *testing.T) {
	h := newHarness(t)
	fx := newGoalRunFixture(t, h)
	fx.mem.appendErr = errors.New("transcript write failed")
	ctx := t.Context()
	root := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(root.ID)

	h.exec.fn = func(req ExecutorRequest) (ExecutorResult, error) {
		handoff, outcome, ok := fx.admitRaw(ctx, req.Attempt.SessionID)
		if !ok || outcome.CommitErr == nil {
			t.Fatalf("turn published outcome=%+v, want a failed transcript commit", outcome)
		}
		return ExecutorResult{
			Submitted: true, Evidence: AttemptEvidence{Summary: "committed"}, handoff: handoff,
			Output: AttemptOutput{Summary: "committed", Hash: "h-1"},
		}, nil
	}

	att, err := h.svc.Claim(ctx, root.ID, "w-1", nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := h.worker.Run(ctx, root.ID, att.ID, Actor{Type: ActorWorker}); err != nil {
		t.Fatalf("worker Run: %v", err)
	}
	if len(fx.runs) != 1 {
		t.Fatalf("admitted Runs = %d, want 1", len(fx.runs))
	}
	// The goal transition committed, but the execution's own result is unknown, so
	// the Run is interrupted rather than completed.
	if status := fx.runStatus(ctx, fx.runs[0]); status != agentrun.StatusInterrupted {
		t.Fatalf("Run status = %q, want interrupted for a turn whose output never committed", status)
	}
	if got := fx.mem.appendCount(); got != 0 {
		t.Fatalf("memory appends = %d, want 0 (the configured failure)", got)
	}
	if got := h.get(root.ID); got.Lifecycle != LifecycleDone {
		t.Fatalf("goal lifecycle = %q, want the committed transition to stand", got.Lifecycle)
	}
}

// TestGoalRunTerminal covers the status mapping directly, including the paths a
// scripted executor cannot reach. A real admitted turn supplies the published
// outcome the mapping consults.
func TestGoalRunTerminal(t *testing.T) {
	h := newHarness(t)
	fx := newGoalRunFixture(t, h)
	ctx := t.Context()
	clean := fx.admit(ctx, fx.seedSession(ctx, "goal-terminal-clean-"+uuid.NewString()))

	// A turn whose transcript commit failed publishes a CommitErr outcome.
	fx.mem.appendErr = errors.New("transcript write failed")
	broken, brokenOutcome, ok := fx.admitRaw(ctx, fx.seedSession(ctx, "goal-terminal-broken-"+uuid.NewString()))
	if !ok || brokenOutcome.CommitErr == nil {
		t.Fatalf("outcome=%+v ok=%v, want a failed transcript commit", brokenOutcome, ok)
	}

	for _, tc := range []struct {
		name     string
		handoff  *agentruntime.ExecutionHandoff
		applyErr error
		want     string
	}{
		{name: "committed write and clean turn", handoff: clean, want: agentrun.StatusCompleted},
		{name: "ambiguous commit", handoff: clean, applyErr: errTxCommit, want: agentrun.StatusInterrupted},
		{name: "canceled attempt", handoff: clean, applyErr: context.Canceled, want: agentrun.StatusCanceled},
		{name: "failed write", handoff: clean, applyErr: errors.New("nope"), want: agentrun.StatusFailed},
		{name: "uncommitted transcript", handoff: broken, want: agentrun.StatusInterrupted},
		{name: "no published outcome", handoff: agentruntime.NewExecutionHandoff(), want: agentrun.StatusInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, reason := goalRunTerminal(tc.handoff, tc.applyErr)
			if status != tc.want {
				t.Fatalf("status = %q, want %q", status, tc.want)
			}
			if strings.TrimSpace(reason) == "" {
				t.Fatal("terminal status recorded no reason")
			}
		})
	}
}
