package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
	cron "github.com/robfig/cron/v3"
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

// schedJobArgs is the River payload for a scheduled-job firing: it carries only
// the scheduler job ID. The job's schedule, message, and ownership are resolved
// from the in-memory/DB job record at work time, so prompt/schedule edits take
// effect without rewriting queued jobs.
type schedJobArgs struct {
	JobID string `json:"job_id"`
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
func (w *schedJobWorker) Work(_ context.Context, rjob *river.Job[schedJobArgs]) error {
	job, ok := w.svc.lookupJob(rjob.Args.JobID)
	if !ok {
		// The job was removed between scheduling and firing (e.g. a one-time job
		// cancelled, or a periodic job whose handle was torn down just as a tick
		// landed). Nothing to run.
		w.svc.log.Info("scheduler: river fired for unknown job, skipping", "job_id", rjob.Args.JobID)
		return nil
	}
	if !job.Enabled {
		// The job was disabled after this firing was enqueued. Unlike gocron's
		// in-process removal, River firings already queued can outlive the
		// periodic-handle removal, so the Enabled flag is the fire-time guard.
		return nil
	}
	// Use the service lifecycle context, not River's per-job context, so the
	// dispatch matches the long-lived context gocron's task closure captured.
	w.svc.executeSingleRun(w.svc.lifeCtx(), job, job.UserID, job.Schedule.At != "")
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

// newSchedulerRiverClient builds the River client that both works the scheduler
// queue and hosts its periodic jobs. The worker closes over s so it can reach
// the live job map at fire time; s.river is assigned by the caller once this
// returns.
func newSchedulerRiverClient(s *Service, pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &schedJobWorker{svc: s})

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger: s.log,
		Queues: map[string]river.QueueConfig{
			schedulerQueue: {MaxWorkers: schedulerMaxWorkers},
		},
		Workers: workers,
	})
	if err != nil {
		return nil, fmt.Errorf("create scheduler river client: %w", err)
	}
	return client, nil
}

// scheduleJob registers a job with River and records its registration in
// s.refs. Recurring (cron/every) jobs become River periodic jobs; one-time (at)
// jobs are enqueued as a single durable job scheduled at their timestamp.
// Caller must hold s.mu.
func (s *Service) scheduleJob(ctx context.Context, job Job) error {
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
		s.refs[job.ID] = s.addPeriodic(job.ID, river.PeriodicInterval(d))
		return nil
	case job.Schedule.At != "":
		t, err := time.Parse(time.RFC3339, job.Schedule.At)
		if err != nil {
			return fmt.Errorf("parse at timestamp: %w", err)
		}
		if !t.After(time.Now()) {
			return errOneTimeJobPast
		}
		res, err := s.river.Insert(ctx, schedJobArgs{JobID: job.ID}, &river.InsertOpts{
			Queue:       schedulerQueue,
			ScheduledAt: t.UTC(),
			MaxAttempts: 1,
		})
		if err != nil {
			return fmt.Errorf("enqueue one-time job: %w", err)
		}
		s.refs[job.ID] = schedRef{oneTimeID: res.Job.ID, isOneTime: true}
		return nil
	}
	return fmt.Errorf("scheduler: job %q has empty schedule", job.ID)
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

// lookupJob returns the live in-memory job by ID. The in-memory map is the
// authoritative mirror of scheduled jobs, so a miss means the job is not (or no
// longer) scheduled and a fired River job for it should be ignored.
func (s *Service) lookupJob(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return j, ok
}

// lifeCtx returns the service lifecycle context, falling back to Background
// before Start has set it.
func (s *Service) lifeCtx() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}
