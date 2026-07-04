package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	cron "github.com/robfig/cron/v3"

	appdb "github.com/CherryHQ/stella/internal/db"
)

const (
	// schedulerQueue is the River queue scheduled jobs are enqueued on. Kept
	// distinct so the scheduler's worker pool is isolated from any other River
	// usage on the shared database.
	schedulerQueue = "stella_scheduler"
	// schedulerMaxWorkers bounds concurrent scheduled-job executions per node.
	// Re-entrancy of a single job is guarded separately by tryStartJobRun, so
	// this only caps cross-job parallelism.
	schedulerMaxWorkers = 10
)

// schedJobArgs is the River payload for a scheduled-job firing. JobID identifies
// the scheduler job; At carries the one-time fire timestamp (RFC3339) and is
// empty for recurring jobs. At is part of the args so River's ByArgs uniqueness
// keys a one-time job on (JobID, At): re-inserting the same pending fire after a
// restart deduplicates, while rescheduling to a different time enqueues a fresh
// job. The job's schedule, message, and ownership are resolved from the DB at
// work time, so prompt/schedule edits take effect without rewriting queued jobs.
type schedJobArgs struct {
	JobID string `json:"job_id"`
	At    string `json:"at,omitempty"`
}

// Kind implements river.JobArgs.
func (schedJobArgs) Kind() string { return "stella_scheduler_job" }

// schedJobWorker runs a fired scheduler job by delegating to the Service's
// existing single-run path. It always returns nil: executeSingleRun records its
// own success/error run row, and scheduled jobs are fire-and-forget (no River
// retry), matching the previous gocron semantics.
type schedJobWorker struct {
	river.WorkerDefaults[schedJobArgs]
	svc *Service
}

// Work implements river.Worker.
func (w *schedJobWorker) Work(ctx context.Context, rjob *river.Job[schedJobArgs]) error {
	// Resolve the job fresh from the database rather than the local in-memory
	// map: River workers consume the queue cluster-wide, so a job created,
	// updated, or disabled on another node is only visible in the DB. Reading
	// here means the firing always uses the latest prompt/schedule and honors a
	// disable performed on any node.
	row, err := w.svc.q.GetSchedulerJob(ctx, rjob.Args.JobID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The job was removed between scheduling and firing (e.g. a one-time
			// job cancelled, or a periodic handle torn down just as a tick
			// landed). Nothing to run.
			w.svc.log.Info("scheduler: river fired for removed job, skipping", "job_id", rjob.Args.JobID)
			return nil
		}
		return fmt.Errorf("load scheduler job %s: %w", rjob.Args.JobID, err)
	}
	job := dbRowToJob(row)
	if !job.Enabled {
		// The job was disabled after this firing was enqueued. Unlike gocron's
		// in-process removal, River firings already queued can outlive the
		// periodic-handle removal, so the Enabled flag is the fire-time guard.
		return nil
	}
	// Guard against a stale fire whose schedule no longer matches the job. A
	// recurring fire carries Args.At=="" and a one-time fire carries its
	// timestamp, so Args.At is the schedule's identity. If the schedule was
	// changed (recurring<->one-time, or the one-time time moved) after this fire
	// was enqueued, the two disagree: running it would fire at the wrong time
	// and, because executeSingleRun keys one-time retirement on the current
	// schedule, could disable a freshly rescheduled one-time job.
	if rjob.Args.At != job.Schedule.At {
		w.svc.log.Info("scheduler: river fired with stale schedule, skipping",
			"job_id", rjob.Args.JobID, "fire_at", rjob.Args.At, "job_at", job.Schedule.At)
		return nil
	}
	// Pass River's per-job context so a graceful SoftStopTimeout shutdown can
	// cancel an in-flight dispatch; executeSingleRun detaches an uncancellable
	// context for its run bookkeeping so the run row is always finalized.
	w.svc.executeSingleRun(ctx, job, job.UserID, job.Schedule.At != "")
	return nil
}

// cronSchedule adapts a robfig/cron schedule to River's PeriodicSchedule. The
// 5-field standard parser matches the previous gocron CronJob(spec, false).
type cronSchedule struct{ inner cron.Schedule }

// Next implements river.PeriodicSchedule.
func (c cronSchedule) Next(current time.Time) time.Time { return c.inner.Next(current) }

// schedRef is a handle to a job's live River registration so it can later be
// torn down. A recurring (cron/every) job holds a periodic handle; a one-time
// (at) job holds the River job ID of its pending insertion. The struct is
// comparable so callers can detect a changed registration during a swap.
type schedRef struct {
	periodic  rivertype.PeriodicJobHandle
	oneTimeID int64
	isOneTime bool
}

// SchedulerQueueConfig returns the scheduler's River queue name and per-node
// worker config. The composition root reads it to assemble the single shared
// working client (db.NewWorkingRiverClient) when scheduler runs in external-river
// mode alongside the goal queue.
func SchedulerQueueConfig() (string, river.QueueConfig) {
	return schedulerQueue, river.QueueConfig{MaxWorkers: schedulerMaxWorkers}
}

// RegisterRiverWorker registers the scheduler's job worker into a shared workers
// bundle. The worker closes over svc so it can resolve jobs from the DB at fire
// time. Used both by the self-contained client (newSchedulerRiverClient) and by
// the composition root building the shared working client.
func RegisterRiverWorker(workers *river.Workers, svc *Service) {
	river.AddWorker(workers, &schedJobWorker{svc: svc})
}

// newSchedulerRiverClient builds a self-contained River client that both works
// the scheduler queue and hosts its periodic jobs. Used when the Service owns its
// River client (the default / test path); production injects a shared client via
// SetRiverClient instead (WithExternalRiver). s.river is assigned by the caller.
func newSchedulerRiverClient(s *Service, pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	RegisterRiverWorker(workers, s)
	name, cfg := SchedulerQueueConfig()
	return appdb.NewWorkingRiverClient(pool, map[string]river.QueueConfig{name: cfg}, workers, s.log)
}

// scheduleJob registers a job with River and records its registration in
// s.refs. Recurring (cron/every) jobs become River periodic jobs; one-time (at)
// jobs are enqueued as a single durable job scheduled at their timestamp.
// Caller must hold s.mu. The one-time insert runs on s.lifeCtx() (matching
// unscheduleRef) so it is never handed a nil context before Start.
func (s *Service) scheduleJob(job Job) error {
	switch {
	case job.Schedule.Cron != "":
		sched, err := cron.ParseStandard(job.Schedule.Cron)
		if err != nil {
			return fmt.Errorf("parse cron %q: %w", job.Schedule.Cron, err)
		}
		s.refs[job.ID] = s.addPeriodic(job.ID, cronSchedule{sched})
		return nil
	case job.Schedule.Every != "":
		d, err := time.ParseDuration(job.Schedule.Every)
		if err != nil {
			return fmt.Errorf("parse duration: %w", err)
		}
		if d <= 0 {
			// river.PeriodicInterval(0) hot-loops the enqueuer; guard a corrupt
			// or pre-validation persisted value.
			return fmt.Errorf("scheduler: job %q has non-positive interval %q", job.ID, job.Schedule.Every)
		}
		s.refs[job.ID] = s.addPeriodic(job.ID, river.PeriodicInterval(d))
		return nil
	case job.Schedule.At != "":
		t, err := time.Parse(time.RFC3339, job.Schedule.At)
		if err != nil {
			return fmt.Errorf("parse at timestamp: %w", err)
		}
		if !t.After(time.Now()) {
			return ErrOneTimeJobPast
		}
		res, err := s.river.Insert(s.lifeCtx(), schedJobArgs{JobID: job.ID, At: job.Schedule.At}, &river.InsertOpts{
			Queue:       schedulerQueue,
			ScheduledAt: t.UTC(),
			MaxAttempts: 1,
			UniqueOpts:  schedUniqueOpts(),
		})
		if err != nil {
			return fmt.Errorf("enqueue one-time job: %w", err)
		}
		s.refs[job.ID] = schedRef{oneTimeID: res.Job.ID, isOneTime: true}
		return nil
	}
	return fmt.Errorf("scheduler: job %q has empty schedule", job.ID)
}

// schedUniqueOpts deduplicates in-flight firings of the same job. A new firing
// is skipped while an identical one is still available, pending, running, or
// scheduled, collapsing a backlog of queued ticks (after downtime or a slow
// run) into a single fire — restoring gocron's "skip the tick if the job is
// mid-flight" behavior. Completed is deliberately omitted from ByState: River's
// default includes it, which would block a recurring job forever once it had
// run once.
func schedUniqueOpts() river.UniqueOpts {
	return river.UniqueOpts{
		ByArgs: true,
		ByState: []rivertype.JobState{
			rivertype.JobStateAvailable,
			rivertype.JobStatePending,
			rivertype.JobStateRunning,
			rivertype.JobStateScheduled,
		},
	}
}

// addPeriodic registers a recurring River periodic job that enqueues the given
// scheduler job ID on every tick, returning a schedRef holding its handle.
// MaxAttempts is 1 because executeSingleRun owns retry semantics (there are
// none — a failed fire records an error run and waits for the next tick).
func (s *Service) addPeriodic(jobID string, schedule river.PeriodicSchedule) schedRef {
	handle := s.river.PeriodicJobs().Add(river.NewPeriodicJob(
		schedule,
		func() (river.JobArgs, *river.InsertOpts) {
			return schedJobArgs{JobID: jobID}, &river.InsertOpts{
				Queue:       schedulerQueue,
				MaxAttempts: 1,
				UniqueOpts:  schedUniqueOpts(),
			}
		},
		nil,
	))
	return schedRef{periodic: handle}
}

// unscheduleJob tears down a job's live River registration and forgets it.
// Caller must hold s.mu.
func (s *Service) unscheduleJob(id string) {
	if ref, ok := s.refs[id]; ok {
		s.unscheduleRef(ref)
		delete(s.refs, id)
	}
}

// unscheduleRef removes a single registration: a periodic handle is removed
// from the bundle; a pending one-time job is cancelled so it never fires.
// Caller must hold s.mu.
func (s *Service) unscheduleRef(ref schedRef) {
	if ref.isOneTime {
		if _, err := s.river.JobCancel(s.lifeCtx(), ref.oneTimeID); err != nil {
			s.log.Warn("scheduler: cancel pending one-time river job", "river_job_id", ref.oneTimeID, "error", err)
		}
		return
	}
	s.river.PeriodicJobs().Remove(ref.periodic)
}

// lifeCtx returns the service lifecycle context, falling back to Background
// before Start has set it.
func (s *Service) lifeCtx() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}
