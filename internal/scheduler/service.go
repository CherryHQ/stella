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

	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/notify"
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

// ListActiveUsersFunc returns the IDs of all currently active users for the given org.
// Used by the scheduler to fan out ExecScopeAllUsers jobs.
type ListActiveUsersFunc func(ctx context.Context, orgID string) ([]string, error)

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
	listActiveUsers ListActiveUsersFunc

	// Runtime-registered builtin specs and their handler-mode dispatch table.
	// Populated via (*Service).RegisterBuiltin, distinct from the package-global
	// builtinJobs registry that init() functions write to.
	runtimeBuiltins []BuiltinJob
	builtinHandlers map[string]OnJobFunc

	// Heartbeat (optional, configured via SetHeartbeat).
	heartbeatCfg      *HeartbeatConfig
	heartbeatChat     ChatFunc
	heartbeatNotifier notify.Notifier
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

// SetDefaultOrgID is deprecated and a no-op. OrgID is now set per-job.
func (s *Service) SetDefaultOrgID(_ string) {}

// NewFromPath creates a scheduler service that opens its own SQLite database
// at the given path. The database is closed when Stop is called.
func NewFromPath(dbPath string) (*Service, error) {
	db, err := appdb.OpenDB(dbPath)
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

// SetUserJobsEnabled controls whether persisted user-owned and all_users scheduler jobs are loaded.
func (s *Service) SetUserJobsEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userJobsEnabled = enabled
}

// SetListActiveUsersFunc registers the function used to enumerate active users
// when fanning out ExecScopeAllUsers jobs.
func (s *Service) SetListActiveUsersFunc(fn ListActiveUsersFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listActiveUsers = fn
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
// Use this when the scheduler is only needed for internal tasks such as heartbeat.
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
	if loadPersisted {
		for _, j := range jobs {
			s.jobs[j.ID] = j
			// Skip user-scoped and all_users jobs when user jobs are disabled.
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
// OrgID is read from the context via config.OrgIDFromContext.
func (s *Service) AddJobForContext(ctx context.Context, name, message string, sched Schedule, sessionMode string) (Job, error) {
	userID := memory.UserIDFromContext(ctx)
	agentID := memory.AgentIDFromContext(ctx)
	orgID := config.OrgIDFromContext(ctx)
	execScope := ExecScopeSystem
	if userID != "" {
		execScope = ExecScopeUser
	}
	return s.addJobInternal(name, message, sched, sessionMode, agentID, userID, JobOwnerUser, execScope, orgID)
}

// AddJobWithOwner creates a user-owned job with explicit owner parameters.
func (s *Service) AddJobWithOwner(name, message string, sched Schedule, sessionMode, agentID string, userID string, orgID string) (Job, error) {
	execScope := ExecScopeSystem
	if userID != "" {
		execScope = ExecScopeUser
	}
	return s.addJobInternal(name, message, sched, sessionMode, agentID, userID, JobOwnerUser, execScope, orgID)
}

func (s *Service) addJobInternal(name, message string, sched Schedule, sessionMode, agentID string, userID string, ownerKind, execScope, orgID string) (Job, error) {
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
		OrgID:       orgID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.scheduleJob(s.ctx, job); err != nil {
		return Job{}, fmt.Errorf("schedule job: %w", err)
	}

	if err := s.insertJob(s.ctx, job); err != nil {
		// Roll back: remove from gocron.
		if gid, ok := s.gids[job.ID]; ok {
			_ = s.scheduler.RemoveJob(gid)
			delete(s.gids, job.ID)
		}
		return Job{}, fmt.Errorf("persist job: %w", err)
	}

	s.jobs[job.ID] = job

	s.log.Info("job added", "id", job.ID, "name", name, "exec_scope", execScope, "agent_id", agentID, "user_id", userID)
	return job, nil
}

// AddPluginJob creates, persists, and schedules a plugin-owned job.
// OrgID is read from ctx via config.OrgIDFromContext.
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
		OrgID:       config.OrgIDFromContext(ctx),
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

	orgID := s.jobs[id].OrgID
	if err := s.deleteJob(s.ctx, id, orgID); err != nil {
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
func (s *Service) EnsureJob(name, message string, sched Schedule, sessionMode, agentID, execScope, orgID string) (Job, error) {
	if sessionMode == "" {
		sessionMode = SessionReuse
	}
	if execScope == "" {
		execScope = ExecScopeSystem
	}

	s.mu.Lock()
	for _, j := range s.jobs {
		if j.Name != name || j.OrgID != orgID {
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
	return s.addJobInternal(name, message, sched, sessionMode, agentID, "", JobOwnerSystem, execScope, orgID)
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
		s.executeJob(ctx, captured, isOneTime)
	}))
	if err != nil {
		return err
	}

	s.gids[job.ID] = gj.ID()
	return nil
}

// executeJob dispatches on ExecScope and runs the job with run tracking.
func (s *Service) executeJob(ctx context.Context, job Job, isOneTime bool) {
	if job.ExecScope == ExecScopeAllUsers {
		s.executeJobForAllUsers(ctx, job, isOneTime)
		return
	}
	s.executeSingleRun(ctx, job, job.UserID, isOneTime)
}

// executeJobForAllUsers fans out a job to every active user.
// Sub-runs execute sequentially so that removeOneTimeJob fires only after all
// users have finished. Caller is typically a goroutine, so long fan-outs don't
// block the scheduler.
func (s *Service) executeJobForAllUsers(ctx context.Context, job Job, isOneTime bool) {
	s.mu.Lock()
	fn := s.listActiveUsers
	s.mu.Unlock()

	if fn == nil {
		s.log.Warn("all_users job has no listActiveUsers configured, skipping", "job_id", job.ID)
		return
	}

	userIDs, err := fn(ctx, job.OrgID)
	if err != nil {
		s.log.Warn("failed to list active users for all_users job", "job_id", job.ID, "error", err)
		return
	}

	for _, uid := range userIDs {
		s.executeSingleRun(ctx, job, uid, false)
	}

	if isOneTime {
		go s.removeOneTimeJob(job.ID)
	}
}

// executeSingleRun runs one job execution for the given userID (empty = system context).
func (s *Service) executeSingleRun(ctx context.Context, job Job, userID string, isOneTime bool) {
	var sessionID string
	if userID != "" {
		sessionID = job.UserSessionID(userID)
	} else {
		sessionID = job.SessionID()
	}

	runID := uuid.New().String()[:8]
	startedAt := time.Now().UTC()

	if err := s.createJobRun(ctx, runID, job.ID, sessionID, userID, startedAt); err != nil {
		s.log.Warn("failed to create job run record", "job_id", job.ID, "error", err)
	}

	runCtx := WithRunSessionID(ctx, sessionID)

	// Inject user into job copy so the callback can read job.UserID correctly.
	jobRun := job
	jobRun.UserID = userID

	s.mu.Lock()
	handler, hasHandler := s.builtinHandlers[job.Name]
	fn := s.onJob
	listeners := append([]OnJobFunc(nil), s.listeners...)
	s.mu.Unlock()

	var runErr error
	if hasHandler {
		// Handler-mode builtin: bypass the default agent dispatch so the Go
		// callback owns the run entirely.
		if err := handler(runCtx, jobRun); err != nil {
			runErr = err
		}
	} else if fn != nil {
		if err := fn(runCtx, jobRun); err != nil {
			runErr = err
		}
	}
	for _, listener := range listeners {
		if err := listener(runCtx, jobRun); err != nil && runErr == nil {
			runErr = err
		}
	}

	finishedAt := time.Now().UTC()
	status := RunStatusSuccess
	errStr := ""
	if runErr != nil {
		status = RunStatusError
		errStr = runErr.Error()
	}

	if err := s.finishJobRun(ctx, runID, job.ID, status, finishedAt, errStr); err != nil {
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

	if err := s.recordJobRun(ctx, job.ID, job.OrgID, finishedAt, runErr); err != nil {
		s.log.Warn("failed to record scheduler job run", "id", job.ID, "error", err)
	}

	if isOneTime {
		go s.removeOneTimeJob(job.ID)
	}
}

// RunJobNow triggers an immediate execution of the given job asynchronously.
// For ExecScopeAllUsers jobs all user sub-runs are launched; no run ID is returned.
// For other scopes, returns the run ID of the newly created run record.
// Returns errJobAlreadyRunning (wrapped) if a run is already active for the job.
func (s *Service) RunJobNow(ctx context.Context, jobID string) (string, error) {
	s.mu.Lock()
	job, ok := s.jobs[jobID]
	svcCtx := s.ctx
	s.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("job %q not found", jobID)
	}

	if job.ExecScope == ExecScopeAllUsers {
		go s.executeJobForAllUsers(svcCtx, job, false)
		return "", nil
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
		runCtx := WithRunSessionID(svcCtx, sessionID)

		s.mu.Lock()
		handler, hasHandler := s.builtinHandlers[job.Name]
		fn := s.onJob
		listeners := append([]OnJobFunc(nil), s.listeners...)
		s.mu.Unlock()

		var runErr error
		if hasHandler {
			if err := handler(runCtx, job); err != nil {
				runErr = err
			}
		} else if fn != nil {
			if err := fn(runCtx, job); err != nil {
				runErr = err
			}
		}
		for _, listener := range listeners {
			if err := listener(runCtx, job); err != nil && runErr == nil {
				runErr = err
			}
		}

		finishedAt := time.Now().UTC()
		status := RunStatusSuccess
		errStr := ""
		if runErr != nil {
			status = RunStatusError
			errStr = runErr.Error()
		}

		if err := s.finishJobRun(svcCtx, runID, jobID, status, finishedAt, errStr); err != nil {
			s.log.Warn("failed to finish job run record", "run_id", runID, "error", err)
		}
		if err := s.recordJobRun(svcCtx, jobID, job.OrgID, finishedAt, runErr); err != nil {
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
func (s *Service) ListJobRuns(ctx context.Context, jobID string, limit int) ([]JobRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.q.ListSchedJobRuns(ctx, sqlc.ListSchedJobRunsParams{
		JobID: jobID,
		Limit: int64(limit),
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
	orgID := ""
	if j, ok := s.jobs[id]; ok {
		orgID = j.OrgID
	}
	if err := s.deleteJob(s.ctx, id, orgID); err != nil {
		s.log.Warn("failed to remove one-time job after execution", "id", id, "error", err)
	} else {
		s.log.Info("one-time job auto-removed after execution", "id", id)
	}
	delete(s.jobs, id)
}
