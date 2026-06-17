package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// errOneTimeJobPast is returned by scheduleJob when a one-time job's timestamp
// has already elapsed. Start suppresses this for persisted jobs; AddJob treats
// it as a hard failure.
var errOneTimeJobPast = errors.New("one-time job timestamp is in the past")

// errJobAlreadyRunning is returned by RunJobNow when the job has an active run.
var errJobAlreadyRunning = errors.New("job already has a run in progress")

// OnJobFunc is called when a scheduled job fires.
type OnJobFunc func(ctx context.Context, job Job) error

// TaskFunc is a lightweight scheduled callback that is not persisted as a scheduled job.
type TaskFunc func(ctx context.Context)

// Service manages scheduled jobs backed by gocron/v2 with database persistence.
type Service struct {
	scheduler       gocron.Scheduler
	onJob           OnJobFunc
	listeners       []OnJobFunc
	db              *sql.DB
	q               *sqlc.Queries
	ownsDB          bool            // true when Service opened the DB itself
	ctx             context.Context // lifecycle context from Start
	mu              sync.Mutex
	jobs            map[string]Job
	gids            map[string]uuid.UUID // job ID -> gocron job UUID
	log             *slog.Logger
	userJobsEnabled bool

	// Runtime-registered builtin specs, keyed by Name. Populated via
	// (*Service).RegisterBuiltin and distinct from the package-global
	// builtinJobs registry that init() functions write to. Handler-mode
	// dispatch reads the Handler off this map at fire time.
	runtimeBuiltins map[string]BuiltinJob

	// templates holds job template specs registered via RegisterTemplate.
	// Subscription instances store the template key in job_key and resolve
	// their prompt here at fire time.
	templates map[string]JobTemplate

	// started flips to true inside start(); RegisterBuiltin rejects further
	// runtime registrations after that point to avoid persisted handler-mode
	// jobs firing before their handler exists.
	started bool
}

// New creates a scheduler service backed by the given database.
// Call Start to load persisted jobs and begin scheduling.
func New(db *sql.DB) (*Service, error) {
	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("create scheduler: %w", err)
	}
	return &Service{
		scheduler:       s,
		db:              db,
		q:               sqlc.New(db),
		jobs:            make(map[string]Job),
		gids:            make(map[string]uuid.UUID),
		log:             slog.With("component", "scheduler"),
		userJobsEnabled: true,
	}, nil
}

// NewFromPath creates a scheduler service that opens its own PostgreSQL
// database from the given DSN. The database is closed when Stop is called.
func NewFromPath(dsn string) (*Service, error) {
	db, err := appdb.OpenDB(dsn)
	if err != nil {
		return nil, err
	}
	svc, err := New(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	svc.ownsDB = true
	return svc, nil
}

// SetUserJobsEnabled controls whether persisted user-owned scheduler jobs are loaded.
func (s *Service) SetUserJobsEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userJobsEnabled = enabled
}

// SetOnJob sets the primary callback invoked when a job fires.
func (s *Service) SetOnJob(fn OnJobFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onJob = fn
}

// AddOnJobListener appends an additional callback invoked when a job fires.
func (s *Service) AddOnJobListener(fn OnJobFunc) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
}

// Start loads persisted jobs and starts the scheduler.
func (s *Service) Start(ctx context.Context) error {
	return s.start(ctx, true)
}

// StartEphemeral starts the shared scheduler without loading persisted jobs.
func (s *Service) StartEphemeral(ctx context.Context) error {
	return s.start(ctx, false)
}

func (s *Service) start(ctx context.Context, loadPersisted bool) error {
	var jobs []Job
	var err error
	if loadPersisted {
		jobs, err = s.loadJobs(ctx)
		if err != nil {
			return fmt.Errorf("load jobs: %w", err)
		}
	}

	s.mu.Lock()
	s.ctx = ctx
	s.started = true
	if loadPersisted {
		for _, j := range jobs {
			s.jobs[j.ID] = j
			// Skip user-scoped jobs when user jobs are disabled.
			if !s.userJobsEnabled && j.ExecScope != ExecScopeSystem {
				continue
			}
			if j.Enabled {
				if err := s.scheduleJob(ctx, j); err != nil {
					if errors.Is(err, errOneTimeJobPast) {
						s.log.Info("skipping one-time job with past timestamp", "id", j.ID, "at", j.Schedule.At)
					} else {
						s.log.Warn("failed to schedule persisted job", "id", j.ID, "name", j.Name, "error", err)
					}
				}
			}
		}
	}
	s.mu.Unlock()

	s.scheduler.Start()
	if loadPersisted {
		s.log.Info("scheduler service started", "jobs", len(jobs))
	} else {
		s.log.Info("scheduler service started without persisted jobs")
	}
	return nil
}

// Stop shuts down the scheduler and closes the database if owned.
//
// Service is single-shot: Start after Stop is not supported. gocron's
// Shutdown is terminal, and s.started is not reset, so any subsequent
// RegisterBuiltin will reject with "called after Start". Construct a new
// Service if you need a fresh lifecycle.
func (s *Service) Stop() error {
	err := s.scheduler.Shutdown()
	if s.ownsDB && s.db != nil {
		if dbErr := s.db.Close(); dbErr != nil && err == nil {
			err = dbErr
		}
	}
	return err
}

// ScheduleEvery registers a non-persisted recurring task on the existing scheduler.
func (s *Service) ScheduleEvery(ctx context.Context, every string, fn TaskFunc) error {
	if every == "" {
		return fmt.Errorf("every is required")
	}
	if fn == nil {
		return fmt.Errorf("task function is required")
	}

	d, err := time.ParseDuration(every)
	if err != nil {
		return fmt.Errorf("parse duration: %w", err)
	}

	_, err = s.scheduler.NewJob(gocron.DurationJob(d), gocron.NewTask(func() {
		fn(ctx)
	}))
	if err != nil {
		return fmt.Errorf("schedule task: %w", err)
	}

	return nil
}

// AddJobForContext creates a user-owned job bound to the current execution context.
func (s *Service) AddJobForContext(ctx context.Context, name, message string, sched Schedule, sessionMode string) (Job, error) {
	userID := memory.UserIDFromContext(ctx)
	agentID := memory.AgentIDFromContext(ctx)
	execScope := ExecScopeSystem
	if userID != "" {
		execScope = ExecScopeUser
	}
	return s.addJobInternal(name, message, sched, sessionMode, agentID, userID, JobOwnerUser, execScope)
}

// AddJobWithOwner creates a user-owned job with explicit owner parameters.
func (s *Service) AddJobWithOwner(name, message string, sched Schedule, sessionMode, agentID string, userID string) (Job, error) {
	execScope := ExecScopeSystem
	if userID != "" {
		execScope = ExecScopeUser
	}
	return s.addJobInternal(name, message, sched, sessionMode, agentID, userID, JobOwnerUser, execScope)
}

func (s *Service) addJobInternal(name, message string, sched Schedule, sessionMode, agentID string, userID string, ownerKind, execScope string) (Job, error) {
	if name == "" {
		return Job{}, fmt.Errorf("name is required")
	}
	// Handler-mode system builtins (e.g. reflect-review) carry no agent
	// message; they invoke a Go callback directly. Only require a message
	// for jobs that actually dispatch through the agent pool.
	if message == "" && ownerKind != JobOwnerSystem {
		return Job{}, fmt.Errorf("message is required")
	}
	if err := validateSchedule(sched); err != nil {
		return Job{}, err
	}
	// Reject non-system jobs whose name collides with a registered builtin or
	// template. Otherwise the user's prompt would be silently dropped at
	// dispatch time — the handler-mode router keys on Name alone.
	if ownerKind != JobOwnerSystem && s.nameIsReservedBuiltin(name) {
		return Job{}, fmt.Errorf("job name %q is reserved for a builtin", name)
	}

	if sessionMode == "" {
		sessionMode = SessionReuse
	}
	if sessionMode != SessionReuse && sessionMode != SessionNew {
		return Job{}, fmt.Errorf("invalid session_mode %q: must be %q or %q", sessionMode, SessionReuse, SessionNew)
	}

	now := time.Now().UTC()
	job := Job{
		ID:          uuid.New().String()[:8],
		OwnerKind:   ownerKind,
		ExecScope:   normalizeExecScope(execScope),
		Name:        name,
		Schedule:    sched,
		Message:     message,
		SessionMode: sessionMode,
		Enabled:     true,
		AgentID:     agentID,
		UserID:      userID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.addJobLocked(job); err != nil {
		return Job{}, err
	}

	s.log.Info("job added", "id", job.ID, "name", name, "exec_scope", execScope, "agent_id", agentID, "user_id", userID)
	return job, nil
}

// AddPluginJob creates, persists, and schedules a plugin-owned job.
func (s *Service) AddPluginJob(ctx context.Context, pluginID, key, runtimeName, name, description string, sched Schedule, payload map[string]any) (Job, error) {
	if pluginID == "" {
		return Job{}, fmt.Errorf("plugin_id is required")
	}
	if key == "" {
		return Job{}, fmt.Errorf("job key is required")
	}
	if runtimeName == "" {
		return Job{}, fmt.Errorf("runtime name is required")
	}
	if name == "" {
		return Job{}, fmt.Errorf("name is required")
	}
	if err := validateSchedule(sched); err != nil {
		return Job{}, err
	}
	now := time.Now().UTC()
	job := Job{
		ID:          uuid.New().String()[:8],
		OwnerKind:   JobOwnerPlugin,
		ExecScope:   ExecScopeSystem,
		PluginID:    pluginID,
		JobKey:      key,
		RuntimeName: runtimeName,
		Name:        name,
		Description: description,
		Schedule:    sched,
		Payload:     clonePayload(payload),
		SessionMode: SessionReuse,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.scheduleJob(s.ctx, job); err != nil {
		return Job{}, fmt.Errorf("schedule job: %w", err)
	}
	if err := s.insertJob(s.ctx, job); err != nil {
		if gid, ok := s.gids[job.ID]; ok {
			_ = s.scheduler.RemoveJob(gid)
			delete(s.gids, job.ID)
		}
		return Job{}, fmt.Errorf("persist job: %w", err)
	}
	s.jobs[job.ID] = job
	return job, nil
}

func validateSchedule(sched Schedule) error {
	setCount := 0
	if sched.Cron != "" {
		setCount++
	}
	if sched.Every != "" {
		setCount++
	}
	if sched.At != "" {
		setCount++
	}
	if setCount == 0 {
		return fmt.Errorf("schedule requires one of cron, every, or at")
	}
	if setCount > 1 {
		return fmt.Errorf("schedule must have exactly one of cron, every, or at")
	}
	if sched.Every != "" {
		if _, err := time.ParseDuration(sched.Every); err != nil {
			return fmt.Errorf("invalid duration %q: %w", sched.Every, err)
		}
	}
	if sched.At != "" {
		t, err := time.Parse(time.RFC3339, sched.At)
		if err != nil {
			return fmt.Errorf("invalid at timestamp %q: must be RFC3339 format: %w", sched.At, err)
		}
		if !t.After(time.Now()) {
			return fmt.Errorf("at timestamp %q is in the past", sched.At)
		}
	}
	return nil
}

// RemoveJob unschedules and removes a job.
func (s *Service) RemoveJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.jobs[id]; !ok {
		return fmt.Errorf("job %q not found", id)
	}

	// Remove from scheduler first.
	if gid, ok := s.gids[id]; ok {
		if err := s.scheduler.RemoveJob(gid); err != nil {
			s.log.Warn("failed to remove gocron job", "id", id, "error", err)
		}
		delete(s.gids, id)
	}

	if err := s.deleteJob(s.ctx, id); err != nil {
		return fmt.Errorf("persist after remove: %w", err)
	}

	delete(s.jobs, id)

	s.log.Info("job removed", "id", id)
	return nil
}

// EnsureJob creates a job if no job with the same name exists, or updates
// the existing job when any field has changed.
// It is intended for builtin jobs that should be seeded on startup.
// Jobs created by EnsureJob are owned by the system (JobOwnerSystem).
func (s *Service) EnsureJob(name, message string, sched Schedule, sessionMode, agentID, execScope string) (Job, error) {
	if sessionMode == "" {
		sessionMode = SessionReuse
	}
	if execScope == "" {
		execScope = ExecScopeSystem
	}

	s.mu.Lock()
	for _, j := range s.jobs {
		if j.Name != name {
			continue
		}
		if j.Message == message && j.Schedule == sched && j.SessionMode == sessionMode && j.AgentID == agentID && j.ExecScope == execScope {
			s.mu.Unlock()
			return j, nil
		}
		j.Message = message
		j.Schedule = sched
		j.SessionMode = sessionMode
		j.AgentID = agentID
		j.ExecScope = normalizeExecScope(execScope)
		j.UpdatedAt = time.Now().UTC()

		if gid, ok := s.gids[j.ID]; ok {
			_ = s.scheduler.RemoveJob(gid)
			delete(s.gids, j.ID)
		}
		if err := s.scheduleJob(s.ctx, j); err != nil {
			s.mu.Unlock()
			return Job{}, fmt.Errorf("reschedule job: %w", err)
		}
		s.jobs[j.ID] = j
		s.mu.Unlock()

		if err := s.updateJob(s.ctx, j); err != nil {
			return Job{}, fmt.Errorf("persist job update: %w", err)
		}
		s.log.Info("builtin job updated", "id", j.ID, "name", name)
		return j, nil
	}
	s.mu.Unlock()
	return s.addJobInternal(name, message, sched, sessionMode, agentID, "", JobOwnerSystem, execScope)
}

// ListJobs returns all jobs.
func (s *Service) ListJobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		result = append(result, j)
	}
	return result
}

// scheduleJob registers a job with gocron. Caller must hold s.mu.
func (s *Service) scheduleJob(ctx context.Context, job Job) error {
	var jobDef gocron.JobDefinition
	switch {
	case job.Schedule.Cron != "":
		jobDef = gocron.CronJob(job.Schedule.Cron, false)
	case job.Schedule.Every != "":
		d, err := time.ParseDuration(job.Schedule.Every)
		if err != nil {
			return fmt.Errorf("parse duration: %w", err)
		}
		jobDef = gocron.DurationJob(d)
	case job.Schedule.At != "":
		t, err := time.Parse(time.RFC3339, job.Schedule.At)
		if err != nil {
			return fmt.Errorf("parse at timestamp: %w", err)
		}
		if !t.After(time.Now()) {
			return errOneTimeJobPast
		}
		jobDef = gocron.OneTimeJob(gocron.OneTimeJobStartDateTime(t))
	}

	captured := job
	isOneTime := job.Schedule.At != ""
	gj, err := s.scheduler.NewJob(jobDef, gocron.NewTask(func() {
		s.executeSingleRun(ctx, captured, captured.UserID, isOneTime)
	}))
	if err != nil {
		return err
	}

	s.gids[job.ID] = gj.ID()
	return nil
}

// executeSingleRun runs one job execution for the given userID (empty = system context).
// Uses tryStartJobRun to guard against re-entrant runs: if a run for this job
// is still active (e.g. previous cron tick overran), this tick is skipped.
func (s *Service) executeSingleRun(ctx context.Context, job Job, userID string, isOneTime bool) {
	var sessionID string
	if userID != "" {
		sessionID = job.UserSessionID(userID)
	} else {
		sessionID = job.SessionID()
	}

	runID := uuid.New().String()[:8]
	startedAt := time.Now().UTC()

	// Atomically check for an existing active run. Skip this tick rather than
	// stacking a second execution on top of one that has not finished yet.
	if err := s.tryStartJobRun(ctx, runID, job.ID, sessionID, userID, startedAt); err != nil {
		if errors.Is(err, errJobAlreadyRunning) {
			s.log.Info("skipping scheduled fire: previous run still active", "job_id", job.ID, "name", job.Name)
			return
		}
		s.log.Warn("failed to create job run record", "job_id", job.ID, "error", err)
		return
	}

	outputSink := &RunOutputSink{}
	runCtx := withRunOutputSink(WithRunSessionID(ctx, sessionID), outputSink)

	// Inject user into job copy so the callback can read job.UserID correctly.
	jobRun := job
	jobRun.UserID = userID

	runErr := s.dispatchJob(runCtx, jobRun)

	finishedAt := time.Now().UTC()
	status := RunStatusSuccess
	errStr := ""
	if runErr != nil {
		status = RunStatusError
		errStr = runErr.Error()
	}

	if err := s.finishJobRun(ctx, runID, job.ID, status, finishedAt, errStr, outputSink.get()); err != nil {
		s.log.Warn("failed to finish job run record", "run_id", runID, "error", err)
	}

	s.mu.Lock()
	if jobState, ok := s.jobs[job.ID]; ok {
		jobState.LastRunAt = &finishedAt
		if runErr != nil {
			jobState.LastError = runErr.Error()
		} else {
			jobState.LastError = ""
		}
		jobState.UpdatedAt = finishedAt
		s.jobs[job.ID] = jobState
	}
	s.mu.Unlock()

	if err := s.recordJobRun(ctx, job.ID, finishedAt, runErr); err != nil {
		s.log.Warn("failed to record scheduler job run", "id", job.ID, "error", err)
	}

	if isOneTime {
		go s.removeOneTimeJob(job.ID)
	}
}

// RunJobNow triggers an immediate execution of the given job asynchronously.
// Returns the run ID of the newly created run record.
// Returns errJobAlreadyRunning (wrapped) if a run is already active for the job.
func (s *Service) RunJobNow(ctx context.Context, jobID string) (string, error) {
	s.mu.Lock()
	job, ok := s.jobs[jobID]
	svcCtx := s.ctx
	s.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("job %q not found", jobID)
	}

	sessionID := job.SessionID()
	if job.UserID != "" {
		sessionID = job.UserSessionID(job.UserID)
	}
	runID := uuid.New().String()[:8]
	startedAt := time.Now().UTC()

	// Atomically check for an existing active run and create the new record.
	if err := s.tryStartJobRun(ctx, runID, jobID, sessionID, job.UserID, startedAt); err != nil {
		if errors.Is(err, errJobAlreadyRunning) {
			return "", fmt.Errorf("job %q already has a run in progress", jobID)
		}
		return "", fmt.Errorf("create run record: %w", err)
	}

	go func() {
		outputSink := &RunOutputSink{}
		runCtx := withRunOutputSink(WithRunSessionID(svcCtx, sessionID), outputSink)
		runErr := s.dispatchJob(runCtx, job)

		finishedAt := time.Now().UTC()
		status := RunStatusSuccess
		errStr := ""
		if runErr != nil {
			status = RunStatusError
			errStr = runErr.Error()
		}

		if err := s.finishJobRun(svcCtx, runID, jobID, status, finishedAt, errStr, outputSink.get()); err != nil {
			s.log.Warn("failed to finish job run record", "run_id", runID, "error", err)
		}
		if err := s.recordJobRun(svcCtx, jobID, finishedAt, runErr); err != nil {
			s.log.Warn("failed to record scheduler job run", "id", jobID, "error", err)
		}

		s.mu.Lock()
		if jobState, ok := s.jobs[jobID]; ok {
			jobState.LastRunAt = &finishedAt
			if runErr != nil {
				jobState.LastError = runErr.Error()
			} else {
				jobState.LastError = ""
			}
			jobState.UpdatedAt = finishedAt
			s.jobs[jobID] = jobState
		}
		s.mu.Unlock()
	}()

	return runID, nil
}

// ListJobRuns returns recent runs for a job.
// ListJobRuns returns the latest `limit` runs for a job (first page only,
// across all users). HTTP callers paginate via the handler's direct query
// instead; this accessor is for internal callers that just want recent runs.
func (s *Service) ListJobRuns(ctx context.Context, jobID string, limit int) ([]JobRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.q.ListSchedJobRuns(ctx, sqlc.ListSchedJobRunsParams{
		JobID:  jobID,
		UserID: nil,
		Limit:  int32(limit),
		Offset: 0,
	})
	if err != nil {
		return nil, err
	}
	runs := make([]JobRun, 0, len(rows))
	for _, r := range rows {
		runs = append(runs, dbRowToJobRun(r))
	}
	return runs, nil
}

// removeOneTimeJob cleans up a one-time job after it fires.
func (s *Service) removeOneTimeJob(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if gid, ok := s.gids[id]; ok {
		_ = s.scheduler.RemoveJob(gid)
		delete(s.gids, id)
	}
	if err := s.deleteJob(s.ctx, id); err != nil {
		s.log.Warn("failed to remove one-time job after execution", "id", id, "error", err)
	} else {
		s.log.Info("one-time job auto-removed after execution", "id", id)
	}
	delete(s.jobs, id)
}

// dispatchJob routes a fired job to its handler-mode callback, the default
// agent OnJob, or an orphan error; then runs every listener. Returns the
// first error seen across primary + listeners.
func (s *Service) dispatchJob(ctx context.Context, job Job) error {
	s.mu.Lock()
	handler := s.runtimeBuiltins[job.Name].Handler
	fn := s.onJob
	listeners := append([]OnJobFunc(nil), s.listeners...)
	s.mu.Unlock()

	// Subscription instances carry an empty message; resolve from the template
	// registry at fire time so prompt improvements propagate automatically.
	if job.OwnerKind == JobOwnerUser && job.JobKey != "" {
		msg, ok := s.ResolveTemplateMessage(job.JobKey)
		if !ok {
			// Template was removed from the binary after the subscription was
			// created. Record a visible error run instead of panicking or
			// silently dropping the execution.
			err := fmt.Errorf("scheduler: template %q not found for subscription job %q", job.JobKey, job.ID)
			s.log.Error("dropping subscription run: template missing", "job_id", job.ID, "template_key", job.JobKey)
			return err
		}
		job.Message = msg
	}

	var runErr error
	switch {
	case handler != nil && job.OwnerKind == JobOwnerSystem:
		// Handler-mode builtin: bypass the default agent dispatch. Gated on
		// system ownership so a user- or plugin-owned job that happens to
		// share a name with a registered builtin cannot hijack the handler.
		runErr = handler(ctx, job)
	case job.OwnerKind == JobOwnerSystem && job.Message == "":
		// Orphan: persisted system job with no message and no live handler
		// (handler-mode builtin whose RegisterBuiltin call was removed in a
		// later build). Don't dispatch an empty prompt to the agent pool.
		runErr = fmt.Errorf("scheduler: system job %q has no handler registered and no message", job.Name)
		s.log.Error("scheduler: dropping orphan system job run", "job_id", job.ID, "name", job.Name)
	case fn != nil:
		runErr = fn(ctx, job)
	}
	for _, listener := range listeners {
		if err := listener(ctx, job); err != nil && runErr == nil {
			runErr = err
		}
	}
	return runErr
}
