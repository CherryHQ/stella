package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// These tests drive REAL Runtime admission through the scheduler's own dispatch
// context, because the scheduler never binds a lease itself: it hands the admitted
// turn a handoff (AgentRuntimeOptionsFromContext) and then owns the run's
// bookkeeping. Only production admission can show that a run whose execution
// ownership was taken away stops writing run/last-run records, and that an
// execution whose own outcome is unknown is not reported as a success.

// schedRunMemory is a minimal memory.Provider, able to fail the transcript commit.
type schedRunMemory struct {
	mu        sync.Mutex
	appendErr error
}

func (m *schedRunMemory) Name() string                                    { return "sched-run-test" }
func (m *schedRunMemory) Bootstrap(context.Context, memory.Session) error { return nil }
func (m *schedRunMemory) Assemble(context.Context, memory.Session, int, int) ([]ai.Message, error) {
	return nil, nil
}

func (m *schedRunMemory) Stats(context.Context, memory.Session) (memory.SessionStats, error) {
	return memory.SessionStats{}, nil
}

func (m *schedRunMemory) Append(context.Context, memory.Session, ...ai.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.appendErr
}

func (m *schedRunMemory) Close() error { return nil }

type schedRunRunner struct{}

func (r *schedRunRunner) Chat(context.Context, []ai.Message, agentruntime.MessageContent) <-chan agentruntime.Event {
	out := make(chan agentruntime.Event, 1)
	out <- agentruntime.Event{Text: "scheduled answer"}
	close(out)
	return out
}

func (r *schedRunRunner) Alive() bool             { return true }
func (r *schedRunRunner) Busy() bool              { return false }
func (r *schedRunRunner) LastActivity() time.Time { return time.Now().UTC() }
func (r *schedRunRunner) SystemPrompt() string    { return "" }
func (r *schedRunRunner) Close() error            { return nil }

func (r *schedRunRunner) PluginContext() agentruntime.PluginContext {
	return agentruntime.PluginContext{}
}

// schedRunFixture owns the Runtime admission the scheduler's dispatch callback
// performs in production (agent.Service.ChatForScheduler -> runtime.Chat under the
// scheduler's handoff option).
type schedRunFixture struct {
	t     *testing.T
	svc   *Service
	store *agentrun.Store
	rt    *agentruntime.Runtime
	mem   *schedRunMemory
	runs  []string
}

func newSchedRunFixture(t *testing.T, svc *Service) *schedRunFixture {
	t.Helper()
	store := agentrun.NewStore(svc.db, agentrun.NewBootID())
	if err := store.RegisterBoot(t.Context()); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	t.Cleanup(store.Close)
	mem := &schedRunMemory{}
	rt, err := agentruntime.New(agentruntime.Config{
		Memory:    mem,
		AgentRuns: store,
		NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
			return &schedRunRunner{}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	return &schedRunFixture{t: t, svc: svc, store: store, mem: mem, rt: rt}
}

// seedSession inserts the durable conversation row a job session resolves to, so
// the runtime can admit a Run for it.
func (f *schedRunFixture) seedSession(ctx context.Context, sessionID string) {
	f.t.Helper()
	if _, err := f.svc.db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1) ON CONFLICT DO NOTHING`, sessionID); err != nil {
		f.t.Fatalf("seed conversation %s: %v", sessionID, err)
	}
}

// dispatch is the scheduler dispatch callback: one real admitted turn on the job's
// session, under the scheduler's own handoff.
func (f *schedRunFixture) dispatch(ctx context.Context) error {
	f.t.Helper()
	handoff, _ := ctx.Value(agentHandoffKey{}).(*agentruntime.ExecutionHandoff)
	if handoff == nil {
		f.t.Fatal("dispatch called without the scheduler's execution handoff")
	}
	stream := f.rt.Chat(ctx, session.Info{
		ID: RunSessionIDFromContext(ctx), UserID: "user-1", AgentID: "agent-a",
	}, "scheduled message", AgentRuntimeOptionsFromContext(ctx)...)
	var runErr error
	for ev := range stream {
		if ev.Err != nil {
			runErr = ev.Err
		}
	}
	if lease := handoff.Lease(); lease != nil {
		f.runs = append(f.runs, lease.Guard.RunID)
	}
	return runErr
}

// expireAndReap removes a run's execution ownership the way a lost heartbeat does.
func (f *schedRunFixture) expireAndReap(ctx context.Context, handoff *agentruntime.ExecutionHandoff) {
	f.t.Helper()
	lease := handoff.Lease()
	if lease == nil {
		f.t.Fatal("expireAndReap called without an admitted Run")
	}
	if _, err := f.svc.db.Exec(ctx, `UPDATE agent_run SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, lease.Guard.RunID); err != nil {
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

func (f *schedRunFixture) runStatus(ctx context.Context, runID string) string {
	f.t.Helper()
	row, err := sqlc.New(f.svc.db).GetAgentRun(ctx, runID)
	if err != nil {
		f.t.Fatalf("get Run %s: %v", runID, err)
	}
	return row.Status
}

// durableJob reads the job's durable record, which is where a scheduler run's
// last-run/last-error bookkeeping lands.
func durableJob(t *testing.T, svc *Service, jobID string) sqlc.SchedJob {
	t.Helper()
	row, err := svc.q.GetSchedulerJob(t.Context(), jobID)
	if err != nil {
		t.Fatalf("get job %s: %v", jobID, err)
	}
	return row
}

func lastJobRun(t *testing.T, svc *Service, jobID string) sqlc.SchedJobRun {
	t.Helper()
	rows, err := svc.q.ListSchedJobRuns(t.Context(), sqlc.ListSchedJobRunsParams{
		JobID: jobID, UserID: pgtype.Text{String: "user-1", Valid: true}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list job runs: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("job runs = %d, want exactly one", len(rows))
	}
	return rows[0]
}

// TestExecuteSingleRunRequiresTheLiveRunFence proves the scheduler's own run
// bookkeeping is fenced by the admitted execution: once recovery has taken the Run
// away, the job-run record and the job's last-run state must not be written.
// Unfenced, these are ordinary management writes and would apply.
func TestExecuteSingleRunRequiresTheLiveRunFence(t *testing.T) {
	svc := testService(t)
	fx := newSchedRunFixture(t, svc)
	ctx := t.Context()

	job := addTestJob(t, svc, "stale-fence", "hello", Schedule{Every: "24h"}, SessionReuse)
	fx.seedSession(ctx, job.UserSessionID("user-1"))

	reaped := false
	svc.SetOnJob(func(jobCtx context.Context, job Job, _ authz.Authority) error {
		handoff, _ := jobCtx.Value(agentHandoffKey{}).(*agentruntime.ExecutionHandoff)
		if err := fx.dispatch(jobCtx); err != nil {
			return err
		}
		fx.expireAndReap(jobCtx, handoff)
		reaped = true
		return nil
	})

	before := durableJob(t, svc, job.ID)
	svc.executeSingleRun(ctx, job, "user-1", false)
	if !reaped {
		t.Fatal("the dispatched run never had its ownership taken away")
	}
	if len(fx.runs) != 1 {
		t.Fatalf("admitted Runs = %d, want 1", len(fx.runs))
	}
	if status := fx.runStatus(ctx, fx.runs[0]); status != agentrun.StatusInterrupted {
		t.Fatalf("Run status = %q, want interrupted after recovery", status)
	}
	// The bookkeeping belongs to an execution that no longer owns its Run, so it
	// must not have committed: the job keeps its previous last-run state and the
	// run record is still the initial "running" row.
	after := durableJob(t, svc, job.ID)
	if after.LastRunAt != before.LastRunAt || after.LastError != before.LastError {
		t.Fatalf("job last-run record = %+v, want %+v (refused)", after, before)
	}
	if row := lastJobRun(t, svc, job.ID); row.Status != RunStatusRunning || row.FinishedAt.Valid {
		t.Fatalf("job run = status %q finished=%v, want the initial running record", row.Status, row.FinishedAt.Valid)
	}
	if live := fx.store.LocalRuns(); live != 0 {
		t.Fatalf("local Runs = %d, want 0", live)
	}
}

func TestSchedulerPanicStopsRunRenewal(t *testing.T) {
	svc := testService(t)
	fx := newSchedRunFixture(t, svc)
	job := addTestJob(t, svc, "panic-release", "hello", Schedule{Every: "24h"}, SessionReuse)
	fx.seedSession(t.Context(), job.UserSessionID("user-1"))
	svc.SetOnJob(func(ctx context.Context, _ Job, _ authz.Authority) error {
		if err := fx.dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		panic("dispatch panicked after admission")
	})
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected dispatch panic")
			}
		}()
		svc.executeSingleRun(t.Context(), job, "user-1", false)
	}()
	if live := fx.store.LocalRuns(); live != 0 {
		t.Fatalf("panicking dispatch left %d renewing Runs", live)
	}
}

// TestExecuteSingleRunConsultsTheRuntimeOutcome pins that the scheduler does not
// trust the adapter's event stream alone: a turn whose transcript never committed
// is recorded as an error run and an interrupted Run, never as a success. The
// callback here deliberately reports no error, which is exactly the adapter that
// swallows the failed output commit.
func TestExecuteSingleRunConsultsTheRuntimeOutcome(t *testing.T) {
	svc := testService(t)
	fx := newSchedRunFixture(t, svc)
	fx.mem.appendErr = errors.New("transcript write failed")
	ctx := t.Context()

	job := addTestJob(t, svc, "uncommitted-output", "hello", Schedule{Every: "24h"}, SessionReuse)
	fx.seedSession(ctx, job.UserSessionID("user-1"))

	svc.SetOnJob(func(jobCtx context.Context, job Job, _ authz.Authority) error {
		_ = fx.dispatch(jobCtx)
		return nil
	})

	svc.executeSingleRun(ctx, job, "user-1", false)
	if len(fx.runs) != 1 {
		t.Fatalf("admitted Runs = %d, want 1", len(fx.runs))
	}
	if status := fx.runStatus(ctx, fx.runs[0]); status != agentrun.StatusInterrupted {
		t.Fatalf("Run status = %q, want interrupted for an unprovable execution result", status)
	}
	if row := lastJobRun(t, svc, job.ID); row.Status != RunStatusError {
		t.Fatalf("job run status = %q, want error for an uncommitted output", row.Status)
	}
	if live := fx.store.LocalRuns(); live != 0 {
		t.Fatalf("local Runs = %d, want 0", live)
	}
}

// TestSchedulerRunStatus covers the status mapping directly, including the
// unknown-outcome branch that must not become an ordinary retryable failure.
func TestSchedulerRunStatus(t *testing.T) {
	for _, tc := range []struct {
		name           string
		runErr         error
		finishErr      error
		recordErr      error
		outcomeUnknown bool
		want           string
	}{
		{name: "clean run", want: agentrun.StatusCompleted},
		{name: "canceled run", runErr: context.Canceled, want: agentrun.StatusCanceled},
		{name: "definite failure", runErr: errors.New("boom"), want: agentrun.StatusFailed},
		{name: "unknown outcome", runErr: errors.New("unknown"), outcomeUnknown: true, want: agentrun.StatusInterrupted},
		{name: "bookkeeping failure", finishErr: errors.New("nope"), want: agentrun.StatusFailed},
		{name: "record failure", recordErr: errors.New("nope"), want: agentrun.StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, reason := schedulerRunStatus(tc.runErr, tc.finishErr, tc.recordErr, tc.outcomeUnknown)
			if status != tc.want {
				t.Fatalf("status = %q, want %q", status, tc.want)
			}
			if reason == "" {
				t.Fatal("terminal status recorded no reason")
			}
		})
	}
}

// TestExecutionErrFromOutcome covers the fold that makes the Runtime's outcome
// authoritative for the job run's own record.
func TestExecutionErrFromOutcome(t *testing.T) {
	if err, unknown := schedulerExecutionErr(nil, agentruntime.TurnOutcome{}, false); err != nil || unknown {
		t.Fatalf("no admitted turn = (%v, %v), want (nil, false)", err, unknown)
	}
	// A turn that never committed its output is unknown, never a clean success.
	err, unknown := schedulerExecutionErr(nil, agentruntime.TurnOutcome{
		Status: agentrun.StatusFailed, CommitErr: errors.New("write failed"),
	}, true)
	if err == nil || !unknown {
		t.Fatalf("uncommitted output = (%v, %v), want an unknown execution error", err, unknown)
	}
	// The adapter's own error is a definite failure, not an ambiguity.
	err, unknown = schedulerExecutionErr(errors.New("adapter error"), agentruntime.TurnOutcome{
		Status: agentrun.StatusFailed, CommitErr: errors.New("write failed"),
	}, true)
	if err == nil || unknown || err.Error() != "adapter error" {
		t.Fatalf("adapter error = (%v, %v), want the definite adapter failure", err, unknown)
	}
	// A clean, committed turn is a success.
	if err, unknown := schedulerExecutionErr(nil, agentruntime.TurnOutcome{Status: agentrun.StatusCompleted}, true); err != nil || unknown {
		t.Fatalf("clean turn = (%v, %v), want (nil, false)", err, unknown)
	}
}

// TestRunBookkeepingWritesRequireTheLiveFence pins the fence itself: the two
// bookkeeping writers commit for a live Run and are refused once recovery has
// taken it, and the production helper binds exactly that fence.
func TestRunBookkeepingWritesRequireTheLiveFence(t *testing.T) {
	svc := testService(t)
	ctx := t.Context()
	store := agentrun.NewStore(svc.db, agentrun.NewBootID())
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatalf("register executor boot: %v", err)
	}
	t.Cleanup(store.Close)

	sessionID := "sched-fence-" + uuid.NewString()
	if _, err := svc.db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	job := addTestJob(t, svc, "fenced", "hello", Schedule{Every: "24h"}, SessionReuse)
	runID := uuid.NewString()
	startedAt := time.Now().UTC()
	if err := svc.tryStartJobRun(ctx, runID, job.ID, sessionID, "user-1", startedAt); err != nil {
		t.Fatalf("tryStartJobRun: %v", err)
	}
	lease, err := store.Acquire(ctx, sessionID, "scheduler")
	if err != nil {
		t.Fatalf("acquire Run: %v", err)
	}

	// The fence helper the run bookkeeping uses must carry the admitted Run's
	// guard, and it must leave an unadmitted run's management path unfenced.
	fenced := leaseWriteContext(context.WithoutCancel(ctx), lease)
	if _, ok := agentrun.GuardFromContext(fenced); !ok {
		t.Fatal("scheduler bookkeeping context carries no execution guard")
	}
	if _, ok := agentrun.GuardFromContext(runBookkeepingContext(ctx, agentruntime.NewExecutionHandoff())); ok {
		t.Fatal("an unadmitted run's bookkeeping context is fenced")
	}
	if err := svc.finishJobRun(fenced, runID, job.ID, RunStatusSuccess, startedAt, "", "ok"); err != nil {
		t.Fatalf("finishJobRun under a live fence: %v", err)
	}
	if err := svc.recordJobRun(fenced, job.ID, startedAt, nil); err != nil {
		t.Fatalf("recordJobRun under a live fence: %v", err)
	}
	if got := durableJob(t, svc, job.ID).LastRunAt; !got.Valid || !got.Time.UTC().Equal(startedAt.UTC()) {
		t.Fatalf("job last run = %v, want the live-fence write to stand", got)
	}

	// Take the ownership away and repeat: both writes must now be refused.
	if _, err := svc.db.Exec(ctx, `UPDATE agent_run SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, lease.Guard.RunID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	if err := store.Reap(ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if err := svc.finishJobRun(fenced, runID, job.ID, RunStatusError, startedAt, "late", "late"); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("finishJobRun after recovery = %v, want the ownership conflict", err)
	}
	if err := svc.recordJobRun(fenced, job.ID, startedAt.Add(time.Minute), errors.New("late")); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("recordJobRun after recovery = %v, want the ownership conflict", err)
	}
	if row := lastJobRun(t, svc, job.ID); row.Status != RunStatusSuccess || row.Error != "" {
		t.Fatalf("job run = %+v, want the live-fence write to stand and the stale one refused", row)
	}
	if got := durableJob(t, svc, job.ID).LastError; got != "" {
		t.Fatalf("job last error = %q, want the stale write refused", got)
	}
}
