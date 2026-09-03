//go:build system

package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/test/testbed"

	"github.com/jackc/pgx/v5"
)

const (
	// restartFireDelay leaves enough room to prove the original process died
	// before the fire and for its replacement to become ready before the due
	// time. It is not a success wait: the durable rows drive every assertion.
	restartFireDelay   = 45 * time.Second
	restartPollBudget  = 75 * time.Second
	restartQuietWindow = 8 * time.Second
)

// testSchedulerOneTimeJobSurvivesForcedRestart proves the process seam for a
// durable one-shot chat job. It is killed before its future fire time, then a
// replacement using the same PostgreSQL, home, and vault identity must execute
// the existing River fire exactly once. This deliberately does not crash an
// in-flight model call: external side effects cannot honestly be made exactly
// once after an abrupt crash, while the pending durable-fire contract can.
func (h *harness) testSchedulerOneTimeJobSurvivesForcedRestart(t *testing.T) {
	fake := newFakeAnthropic(t)
	ctx, cancel := context.WithTimeout(context.Background(), restartPollBudget+30*time.Second)
	defer cancel()

	const modelID = "claude-sonnet-4-6"
	wantReply := "scheduler restart reply " + h.runID
	wantMessage := "run once after forced restart " + h.runID
	fake.enqueueText(wantReply)
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "anthropic-scheduler-restart-"+h.runID)
	agentID := h.createAgent(t, ctx, providerID+"/"+modelID)

	fireAt := time.Now().UTC().Add(restartFireDelay).Truncate(time.Second)
	jobID := h.createSchedulerRestartJob(t, ctx, agentID, fireAt, wantMessage)
	old := h.proc

	before := h.schedulerRestartSnapshot(t, ctx, jobID)
	if err := before.pendingInvariant(jobID, fireAt.Format(time.RFC3339)); err != nil {
		h.failSchedulerRestart(t, ctx, fake, old, jobID, "durable one-time job was not ready before forced crash: %v", err)
	}
	if len(fake.requests()) != 0 {
		h.failSchedulerRestart(t, ctx, fake, old, jobID, "fake received a model request before the forced crash")
	}
	if !time.Now().Before(fireAt) {
		h.failSchedulerRestart(t, ctx, fake, old, jobID, "job due time %s passed before forced crash", fireAt.Format(time.RFC3339))
	}

	old = h.restartAfterForcedCrash(t)
	// Reloading persisted jobs must deduplicate against the original pending
	// River row. This is the inexpensive non-vacuous recovery guard: removing
	// schedUniqueOpts makes this assertion observe two pending fires.
	afterRestart := h.schedulerRestartSnapshot(t, ctx, jobID)
	if err := afterRestart.pendingInvariant(jobID, fireAt.Format(time.RFC3339)); err != nil {
		h.failSchedulerRestart(t, ctx, fake, old, jobID, "restart changed the pending durable fire: %v", err)
	}

	final := h.awaitSchedulerRestartSuccess(t, ctx, fake, old, jobID, fireAt.Format(time.RFC3339), modelID, wantMessage, wantReply)
	if err := final.successInvariant(jobID, fireAt.Format(time.RFC3339), modelID, wantMessage, wantReply, fake.requests()); err != nil {
		h.failSchedulerRestart(t, ctx, fake, old, jobID, "one-time job did not finish exactly once: %v", err)
	}

	// Keep observing durable state rather than sleeping and checking once: a
	// duplicate fire from restart re-registration must fail at any point in this
	// bounded window.
	quietUntil := time.NewTimer(restartQuietWindow)
	defer quietUntil.Stop()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-quietUntil.C:
			return
		case <-ctx.Done():
			h.failSchedulerRestart(t, ctx, fake, old, jobID, "context expired during duplicate-execution observation: %v", ctx.Err())
		case <-ticker.C:
			snapshot := h.schedulerRestartSnapshot(t, ctx, jobID)
			if err := snapshot.successInvariant(jobID, fireAt.Format(time.RFC3339), modelID, wantMessage, wantReply, fake.requests()); err != nil {
				h.failSchedulerRestart(t, ctx, fake, old, jobID, "duplicate-execution observation failed: %v", err)
			}
		}
	}
}

func (h *harness) createSchedulerRestartJob(t *testing.T, ctx context.Context, agentID string, fireAt time.Time, message string) string {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/agents/"+agentID+"/scheduler/jobs", map[string]any{
		"name":         "restart-once-" + h.runID,
		"message":      message,
		"at":           fireAt.Format(time.RFC3339),
		"session_mode": "reuse",
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST scheduler one-time job = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.LogTail(40))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode scheduler one-time job: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created scheduler one-time job has empty id")
	}
	return created.ID
}

type schedulerRestartSnapshot struct {
	jobExists   bool
	jobEnabled  bool
	scheduleAt  string
	lastRunAt   *time.Time
	lastError   string
	jobRowCount int
	runs        []schedulerRestartRun
	river       []schedulerRestartRiverJob
}

type schedulerRestartRun struct {
	id, status, sessionID, errText, output string
	startedAt                              time.Time
	finishedAt                             *time.Time
}

type schedulerRestartRiverJob struct {
	id                 int64
	state, args, queue string
	attempt            int16
	scheduledAt        time.Time
	finalizedAt        *time.Time
}

func (h *harness) schedulerRestartSnapshot(t *testing.T, ctx context.Context, jobID string) schedulerRestartSnapshot {
	t.Helper()
	var s schedulerRestartSnapshot
	err := h.db.QueryRow(ctx, `
		SELECT enabled, schedule_at, last_run_at, last_error,
		       count(*) OVER ()
		FROM sched_job
		WHERE id = $1`, jobID).Scan(&s.jobEnabled, &s.scheduleAt, &s.lastRunAt, &s.lastError, &s.jobRowCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return s
		}
		t.Fatalf("query scheduler job %s: %v", jobID, err)
	}
	s.jobExists = true

	runRows, err := h.db.Query(ctx, `
		SELECT id, status, session_id, started_at, finished_at, error, output
		FROM sched_job_run
		WHERE job_id = $1
		ORDER BY started_at, id`, jobID)
	if err != nil {
		t.Fatalf("query scheduler runs for %s: %v", jobID, err)
	}
	defer runRows.Close()
	for runRows.Next() {
		var r schedulerRestartRun
		if err := runRows.Scan(&r.id, &r.status, &r.sessionID, &r.startedAt, &r.finishedAt, &r.errText, &r.output); err != nil {
			t.Fatalf("scan scheduler run for %s: %v", jobID, err)
		}
		s.runs = append(s.runs, r)
	}
	if err := runRows.Err(); err != nil {
		t.Fatalf("iterate scheduler runs for %s: %v", jobID, err)
	}

	riverRows, err := h.db.Query(ctx, `
		SELECT id, state::text, attempt, scheduled_at, finalized_at, args::text, queue
		FROM river_job
		WHERE kind = 'stella_scheduler_job'
		  AND args->>'job_id' = $1
		ORDER BY id`, jobID)
	if err != nil {
		t.Fatalf("query River jobs for %s: %v", jobID, err)
	}
	defer riverRows.Close()
	for riverRows.Next() {
		var r schedulerRestartRiverJob
		if err := riverRows.Scan(&r.id, &r.state, &r.attempt, &r.scheduledAt, &r.finalizedAt, &r.args, &r.queue); err != nil {
			t.Fatalf("scan River job for %s: %v", jobID, err)
		}
		s.river = append(s.river, r)
	}
	if err := riverRows.Err(); err != nil {
		t.Fatalf("iterate River jobs for %s: %v", jobID, err)
	}
	return s
}

func (s schedulerRestartSnapshot) pendingInvariant(jobID, wantAt string) error {
	if !s.jobExists || s.jobRowCount != 1 {
		return fmt.Errorf("scheduler job identity %q exists=%t count=%d, want exactly one row", jobID, s.jobExists, s.jobRowCount)
	}
	if !s.jobEnabled || s.scheduleAt != wantAt || s.lastRunAt != nil || s.lastError != "" {
		return fmt.Errorf("scheduler job enabled=%t schedule_at=%q last_run_at=%v last_error=%q, want enabled/%q/nil/empty", s.jobEnabled, s.scheduleAt, s.lastRunAt, s.lastError, wantAt)
	}
	if len(s.runs) != 0 {
		return fmt.Errorf("scheduler run count=%d, want 0 before due time", len(s.runs))
	}
	if len(s.river) != 1 {
		return fmt.Errorf("River job count=%d, want exactly one pending fire", len(s.river))
	}
	r := s.river[0]
	var args struct {
		At    string `json:"at"`
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(r.args), &args); err != nil {
		return fmt.Errorf("decode River job args %q: %w", r.args, err)
	}
	if r.state != "scheduled" || r.queue != "stella_scheduler" || args.At != wantAt || args.JobID != jobID {
		return fmt.Errorf("River job state=%q queue=%q args=%s, want scheduled/stella_scheduler/job_id=%q/at=%q", r.state, r.queue, r.args, jobID, wantAt)
	}
	return nil
}

func (s schedulerRestartSnapshot) successInvariant(jobID, wantAt, modelID, wantMessage, wantReply string, reqs []fakeRequest) error {
	if !s.jobExists || s.jobRowCount != 1 {
		return fmt.Errorf("scheduler job identity %q exists=%t count=%d, want exactly one row", jobID, s.jobExists, s.jobRowCount)
	}
	if s.jobEnabled || s.scheduleAt != wantAt || s.lastRunAt == nil || s.lastError != "" {
		return fmt.Errorf("scheduler job enabled=%t schedule_at=%q last_run_at=%v last_error=%q, want disabled/%q/set/empty", s.jobEnabled, s.scheduleAt, s.lastRunAt, s.lastError, wantAt)
	}
	if len(s.runs) != 1 {
		return fmt.Errorf("scheduler run count=%d, want exactly one", len(s.runs))
	}
	run := s.runs[0]
	if run.status != "success" || run.finishedAt == nil || run.errText != "" {
		return fmt.Errorf("scheduler run id=%s status=%q finished_at=%v error=%q, want success/finished/empty", run.id, run.status, run.finishedAt, run.errText)
	}
	if run.output != wantReply {
		return fmt.Errorf("scheduler run output=%q, want %q", run.output, wantReply)
	}
	if len(s.river) != 1 || s.river[0].state != "completed" || s.river[0].finalizedAt == nil {
		return fmt.Errorf("River terminal rows=%+v, want one completed finalized fire", s.river)
	}
	if len(reqs) != 1 {
		return fmt.Errorf("fake model request count=%d, want exactly one", len(reqs))
	}
	if reqs[0].Model != modelID {
		return fmt.Errorf("fake model request model=%q, want %q", reqs[0].Model, modelID)
	}
	if !strings.Contains(strings.Join(reqs[0].Messages, "\n"), wantMessage) {
		return fmt.Errorf("fake model request did not contain scheduled message %q", wantMessage)
	}
	return nil
}

func (h *harness) awaitSchedulerRestartSuccess(t *testing.T, ctx context.Context, fake *fakeAnthropic, old *testbed.Instance, jobID, wantAt, modelID, wantMessage, wantReply string) schedulerRestartSnapshot {
	t.Helper()
	timer := time.NewTimer(restartPollBudget)
	defer timer.Stop()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot := h.schedulerRestartSnapshot(t, ctx, jobID)
		if err := snapshot.successInvariant(jobID, wantAt, modelID, wantMessage, wantReply, fake.requests()); err == nil {
			return snapshot
		}
		select {
		case <-ctx.Done():
			h.failSchedulerRestart(t, ctx, fake, old, jobID, "context expired waiting for one-time job: %v", ctx.Err())
		case <-timer.C:
			h.failSchedulerRestart(t, ctx, fake, old, jobID, "one-time job did not succeed within %s", restartPollBudget)
		case <-ticker.C:
		}
	}
}

func (h *harness) failSchedulerRestart(t *testing.T, ctx context.Context, fake *fakeAnthropic, old *testbed.Instance, jobID, format string, args ...any) {
	t.Helper()
	t.Fatalf(format+"\n%s", append(args, h.dumpSchedulerRestart(ctx, fake, old, jobID))...)
}

func (h *harness) dumpSchedulerRestart(ctx context.Context, fake *fakeAnthropic, old *testbed.Instance, jobID string) string {
	diagCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	ctx = diagCtx

	var b strings.Builder
	fmt.Fprintf(&b, "scheduler restart diagnostics for job %s\n", jobID)
	if old != nil {
		fmt.Fprintf(&b, "old server log: %s\n%s\n", old.LogPath(), old.LogTail(60))
	}
	if h.proc != nil {
		fmt.Fprintf(&b, "new server log: %s\n%s\n", h.proc.LogPath(), h.proc.LogTail(60))
	}

	rows, err := h.db.Query(ctx, `
		SELECT id, enabled, schedule_at, last_run_at, last_error
		FROM sched_job WHERE id = $1`, jobID)
	if err != nil {
		fmt.Fprintf(&b, "query sched_job: %v\n", err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var id, at, lastErr string
			var enabled bool
			var lastRun *time.Time
			if err := rows.Scan(&id, &enabled, &at, &lastRun, &lastErr); err != nil {
				fmt.Fprintf(&b, "scan sched_job: %v\n", err)
				break
			}
			fmt.Fprintf(&b, "sched_job id=%s enabled=%t schedule_at=%q last_run_at=%v last_error=%q\n", id, enabled, at, lastRun, lastErr)
		}
	}

	runs, err := h.db.Query(ctx, `SELECT id, status, started_at, finished_at, error, output FROM sched_job_run WHERE job_id = $1 ORDER BY started_at, id`, jobID)
	if err != nil {
		fmt.Fprintf(&b, "query sched_job_run: %v\n", err)
	} else {
		defer runs.Close()
		for runs.Next() {
			var id, status, runErr, output string
			var started time.Time
			var finished *time.Time
			if err := runs.Scan(&id, &status, &started, &finished, &runErr, &output); err != nil {
				fmt.Fprintf(&b, "scan sched_job_run: %v\n", err)
				break
			}
			fmt.Fprintf(&b, "sched_job_run id=%s status=%s started=%s finished=%v error=%q output=%q\n", id, status, started.UTC().Format(time.RFC3339), finished, runErr, output)
		}
	}

	river, err := h.db.Query(ctx, `SELECT id, state::text, attempt, scheduled_at, finalized_at, args::text FROM river_job WHERE kind = 'stella_scheduler_job' AND args->>'job_id' = $1 ORDER BY id`, jobID)
	if err != nil {
		fmt.Fprintf(&b, "query river_job: %v\n", err)
	} else {
		defer river.Close()
		for river.Next() {
			var id int64
			var state, riverArgs string
			var attempt int16
			var scheduled time.Time
			var finalized *time.Time
			if err := river.Scan(&id, &state, &attempt, &scheduled, &finalized, &riverArgs); err != nil {
				fmt.Fprintf(&b, "scan river_job: %v\n", err)
				break
			}
			fmt.Fprintf(&b, "river_job id=%d state=%s attempt=%d scheduled=%s finalized=%v args=%s\n", id, state, attempt, scheduled.UTC().Format(time.RFC3339), finalized, riverArgs)
		}
	}

	reqs := fake.requests()
	fmt.Fprintf(&b, "fake model requests=%d", len(reqs))
	for i, req := range reqs {
		fmt.Fprintf(&b, " [%d model=%q tools=%v]", i+1, req.Model, req.ToolNames)
	}
	return b.String()
}
