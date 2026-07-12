package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/CherryHQ/stella/internal/agentaccess"
	"github.com/CherryHQ/stella/internal/authz"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ErrOneTimeJobPast is returned by scheduleJob when a one-time job's timestamp
// has already elapsed. Start suppresses this for persisted jobs; AddJob treats
// it as a hard failure.
var ErrOneTimeJobPast = errors.New("one-time job timestamp is in the past")

// OnJobFunc is called when a scheduled job fires.
type OnJobFunc func(ctx context.Context, job Job) error

type WorkflowRunner interface {
	ValidateScheduledWorkflow(ctx context.Context, req WorkflowValidateRequest) (ScheduledWorkflow, error)
	LatestWorkflowRun(ctx context.Context, req WorkflowLatestRunRequest) (WorkflowRunState, error)
	InstantiateWorkflow(ctx context.Context, req WorkflowInstantiateRequest) (WorkflowInstantiateResult, error)
}

type WorkflowValidateRequest struct {
	UserID     string
	AgentID    string
	WorkflowID string
}

type WorkflowLatestRunRequest struct {
	WorkflowID string
}

type WorkflowInstantiateRequest struct {
	UserID         string
	AgentID        string
	WorkflowID     string
	Inputs         map[string]string
	IdempotencyKey string
}

type ScheduledWorkflow struct {
	ID          string
	FullyFrozen bool
}

type WorkflowRunState struct {
	Found            bool
	Status           string
	IdempotencyKey   string
	RootGoalID       string
	RootGoalTerminal bool
}

type WorkflowInstantiateResult struct {
	RunID      string
	RootGoalID string
}

// TaskFunc is a lightweight scheduled callback that is not persisted as a scheduled job.
type TaskFunc func(ctx context.Context)

// errJobAlreadyRunning is returned by RunJobNow when the job has an active run.
var errJobAlreadyRunning = errors.New("job already has a run in progress")

var (
	ErrWorkflowJobValidation = errors.New("workflow job validation failed")
	ErrWorkflowJobNotFound   = errors.New("workflow not found")
)

// Service manages scheduled jobs backed by River durable queues with database
// persistence.
type Service struct {
	river           *river.Client[pgx.Tx]
	ownsRiver       bool // true when Service built its own River client (default/test); false in external-river mode
	externalRiver   bool // set by WithExternalRiver: caller injects+owns the shared client
	onJob           OnJobFunc
	listeners       []OnJobFunc
	workflowRunner  WorkflowRunner
	db              *pgxpool.Pool
	q               *sqlc.Queries
	authz           authz.Authorizer     // policy decision point for the Access PEP
	agents          *agentaccess.Service // agent read gate reused inside one evaluation
	ownsDB          bool                 // true when Service opened the DB itself
	ctx             context.Context      // lifecycle context from Start
	mu              sync.Mutex
	jobs            map[string]Job
	refs            map[string]schedRef // job ID -> live River registration
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

// Option configures a Service at construction.
type Option func(*Service)

// WithExternalRiver constructs the Service WITHOUT its own River client: the
// caller injects the single process-wide working client via BindRiverClient
// before Start and owns its Start/Stop lifecycle. This is how the composition
// root keeps exactly one electable River client per database while letting both
// the scheduler and the goal subsystem work and enqueue jobs (see
// db.NewWorkingRiverClient). Without this option the Service builds and owns a
// self-contained client (the default / test path).
func WithExternalRiver() Option {
	return func(s *Service) { s.externalRiver = true }
}

// WithAuthorization injects the unified Authorizer and the agent-access gate used
// by the Authority-based Access PEP. Without it, Begin fails closed and every
// transport/tool use case is denied — the raw *Service methods (worker dispatch,
// builtin/ensure jobs) remain callable for trusted internal wiring.
func WithAuthorization(az authz.Authorizer, agents *agentaccess.Service) Option {
	return func(s *Service) {
		s.authz = az
		s.agents = agents
	}
}

// New creates a scheduler service backed by the given database.
// Call Start to load persisted jobs and begin scheduling.
func New(db *pgxpool.Pool, opts ...Option) (*Service, error) {
	s := &Service{
		db:              db,
		q:               sqlc.New(db),
		jobs:            make(map[string]Job),
		refs:            make(map[string]schedRef),
		log:             slog.With("component", "scheduler"),
		userJobsEnabled: true,
	}
	for _, o := range opts {
		o(s)
	}
	if !s.externalRiver {
		client, err := newSchedulerRiverClient(s, db)
		if err != nil {
			return nil, err
		}
		s.river = client
		s.ownsRiver = true
	}
	return s, nil
}

// BindRiverClient injects the shared working River client (external-river mode).
// Call after New(db, WithExternalRiver()) and before Start. The Service uses it
// to enqueue and register periodic jobs but does NOT start or stop it — the
// composition root owns the shared client's lifecycle.
// BindRiverClient binds the shared working River client before start
// (external-river mode). One-shot pre-start bind: rejects a nil client
// (missing), a second bind (duplicate), and any bind after start (late).
func (s *Service) BindRiverClient(c *river.Client[pgx.Tx]) error {
	if c == nil {
		return errors.New("scheduler: BindRiverClient requires a non-nil client")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("scheduler: BindRiverClient after start")
	}
	if s.river != nil {
		return errors.New("scheduler: river client already bound")
	}
	s.river = c
	return nil
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
		db.Close()
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

func (s *Service) SetWorkflowRunner(r WorkflowRunner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workflowRunner = r
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
	// External-river mode: the composition root must inject the shared working
	// client via BindRiverClient before start. Without it, scheduleJob and
	// s.river.Start nil-deref. Fail fast with a clear error instead of panicking,
	// mirroring goal.StartDispatchTick's guard.
	if s.externalRiver && s.river == nil {
		return fmt.Errorf("scheduler: external river mode requires BindRiverClient before start")
	}

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
				if err := s.scheduleJob(j); err != nil {
					if errors.Is(err, ErrOneTimeJobPast) {
						s.log.Info("skipping one-time job with past timestamp", "id", j.ID, "at", j.Schedule.At)
					} else {
						s.log.Warn("failed to schedule persisted job", "id", j.ID, "name", j.Name, "error", err)
					}
				}
			}
		}
	}
	s.mu.Unlock()

	// Own the River lifecycle only when we built the client. In external-river
	// mode the composition root starts/stops the single shared client; we just
	// registered our periodic jobs and one-time inserts above against it.
	if s.ownsRiver {
		if err := s.river.Start(ctx); err != nil {
			return fmt.Errorf("start river client: %w", err)
		}
	}
	if loadPersisted {
		s.log.Info("scheduler service started", "jobs", len(jobs))
	} else {
		s.log.Info("scheduler service started without persisted jobs")
	}
	return nil
}

// Stop shuts down the scheduler and closes the database if owned.
//
// Service is single-shot: Start after Stop is not supported. River's Stop is
// terminal, and s.started is not reset, so any subsequent RegisterBuiltin will
// reject with "called after Start". Construct a new Service if you need a fresh
// lifecycle.
//
// A background context is used so in-flight jobs drain gracefully rather than
// being cancelled mid-run.
func (s *Service) Stop() error {
	var err error
	// Stop the River client only when we own it; the shared client is stopped by
	// the composition root in external-river mode.
	if s.ownsRiver {
		err = s.river.Stop(context.Background())
	}
	if s.ownsDB && s.db != nil {
		s.db.Close()
	}
	return err
}

// ScheduleEvery registers a non-persisted recurring task. Unlike scheduled jobs
// it is not durable and not cluster-coordinated: it runs in-process on a ticker
// bound to ctx (every node that calls this runs its own copy), and stops when
// ctx is cancelled. Used for ephemeral in-memory ticks (e.g. the goal
// dispatcher) that must fire on each node rather than once cluster-wide.
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
	if d <= 0 {
		// time.NewTicker panics on a non-positive duration; reject it here.
		return fmt.Errorf("every must be positive, got %q", every)
	}

	go func() {
		ticker := time.NewTicker(d)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fn(ctx)
			}
		}
	}()

	return nil
}

type addJobSpec struct {
	Name           string
	Message        string
	Schedule       Schedule
	SessionMode    string
	AgentID        string
	UserID         string
	OwnerKind      string
	ExecScope      string
	DispatchKind   string
	Payload        map[string]any
	IdempotencyKey string
	Enabled        bool
}

// AddJobForContext creates a user-owned job bound to the current execution context.
func (s *Service) AddJobForContext(ctx context.Context, name, message string, sched Schedule, sessionMode string) (Job, error) {
	userID := authz.UserIDFromContext(ctx)
	agentID := authz.AgentIDFromContext(ctx)
	execScope := ExecScopeSystem
	if userID != "" {
		execScope = ExecScopeUser
	}
	return s.addJobInternal(addJobSpec{Name: name, Message: message, Schedule: sched, SessionMode: sessionMode, AgentID: agentID, UserID: userID, OwnerKind: JobOwnerUser, ExecScope: execScope, DispatchKind: DispatchKindChat, Enabled: true})
}

// AddJobWithOwner creates a user-owned job with explicit owner parameters.
func (s *Service) AddJobWithOwner(name, message string, sched Schedule, sessionMode, agentID string, userID string) (Job, error) {
	execScope := ExecScopeSystem
	if userID != "" {
		execScope = ExecScopeUser
	}
	return s.addJobInternal(addJobSpec{Name: name, Message: message, Schedule: sched, SessionMode: sessionMode, AgentID: agentID, UserID: userID, OwnerKind: JobOwnerUser, ExecScope: execScope, DispatchKind: DispatchKindChat, Enabled: true})
}

func (s *Service) AddJobWithOwnerIdempotency(name, message string, sched Schedule, sessionMode, agentID string, userID string, idempotencyKey string) (Job, error) {
	execScope := ExecScopeSystem
	if userID != "" {
		execScope = ExecScopeUser
	}
	return s.addJobInternal(addJobSpec{Name: name, Message: message, Schedule: sched, SessionMode: sessionMode, AgentID: agentID, UserID: userID, OwnerKind: JobOwnerUser, ExecScope: execScope, DispatchKind: DispatchKindChat, IdempotencyKey: idempotencyKey, Enabled: true})
}

func (s *Service) AddWorkflowJobWithOwner(ctx context.Context, name string, sched Schedule, sessionMode, agentID string, userID string, workflowID string, inputs map[string]string, allowReplan bool) (Job, error) {
	execScope := ExecScopeSystem
	if userID != "" {
		execScope = ExecScopeUser
	}
	payload := map[string]any{"workflow_id": workflowID, "inputs": inputs}
	if allowReplan {
		payload["allow_replan"] = true
	}
	return s.addJobInternal(addJobSpec{Name: name, Schedule: sched, SessionMode: sessionMode, AgentID: agentID, UserID: userID, OwnerKind: JobOwnerUser, ExecScope: execScope, DispatchKind: DispatchKindWorkflow, Payload: payload, Enabled: true})
}

func (s *Service) addJobInternal(spec addJobSpec) (Job, error) {
	if spec.Name == "" {
		return Job{}, fmt.Errorf("name is required")
	}
	if spec.DispatchKind == "" {
		spec.DispatchKind = DispatchKindChat
	}
	payload := clonePayload(spec.Payload)
	if err := s.validateDispatch(s.ctx, spec.DispatchKind, spec.Message, spec.OwnerKind, spec.UserID, spec.AgentID, payload); err != nil {
		return Job{}, err
	}
	if err := validateSchedule(spec.Schedule); err != nil {
		return Job{}, err
	}
	// Reject non-system jobs whose name collides with a registered builtin or
	// template. Otherwise the user's prompt would be silently dropped at
	// dispatch time — the handler-mode router keys on Name alone.
	if spec.OwnerKind != JobOwnerSystem && s.nameIsReservedBuiltin(spec.Name) {
		return Job{}, fmt.Errorf("job name %q is reserved for a builtin", spec.Name)
	}

	if spec.SessionMode == "" {
		spec.SessionMode = SessionReuse
	}
	if spec.SessionMode != SessionReuse && spec.SessionMode != SessionNew {
		return Job{}, fmt.Errorf("invalid session_mode %q: must be %q or %q", spec.SessionMode, SessionReuse, SessionNew)
	}

	now := time.Now().UTC()
	job := Job{
		ID:             uuid.New().String()[:8],
		OwnerKind:      spec.OwnerKind,
		ExecScope:      normalizeExecScope(spec.ExecScope),
		Name:           spec.Name,
		Schedule:       spec.Schedule,
		Message:        spec.Message,
		Payload:        payload,
		DispatchKind:   spec.DispatchKind,
		SessionMode:    spec.SessionMode,
		Enabled:        spec.Enabled,
		AgentID:        spec.AgentID,
		UserID:         spec.UserID,
		IdempotencyKey: spec.IdempotencyKey,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.addJobLocked(job); err != nil {
		return Job{}, err
	}

	s.log.Info("job added", "id", job.ID, "name", spec.Name, "exec_scope", spec.ExecScope, "agent_id", spec.AgentID, "user_id", spec.UserID)
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
		ID:           uuid.New().String()[:8],
		OwnerKind:    JobOwnerPlugin,
		ExecScope:    ExecScopeSystem,
		PluginID:     pluginID,
		JobKey:       key,
		RuntimeName:  runtimeName,
		Name:         name,
		Description:  description,
		Schedule:     sched,
		Payload:      clonePayload(payload),
		DispatchKind: DispatchKindChat,
		SessionMode:  SessionReuse,
		Enabled:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.scheduleJob(job); err != nil {
		return Job{}, fmt.Errorf("schedule job: %w", err)
	}
	if err := s.insertJob(s.ctx, job); err != nil {
		s.unscheduleJob(job.ID)
		return Job{}, fmt.Errorf("persist job: %w", err)
	}
	s.jobs[job.ID] = job
	return job, nil
}

func (s *Service) validateDispatch(ctx context.Context, dispatchKind, message, ownerKind, userID, agentID string, payload map[string]any) error {
	switch dispatchKind {
	case DispatchKindWorkflow:
		if message != "" {
			return fmt.Errorf("%w: message must be empty for workflow jobs", ErrWorkflowJobValidation)
		}
		workflowID, ok := payloadString(payload, "workflow_id")
		if !ok || workflowID == "" {
			return fmt.Errorf("%w: payload.workflow_id is required for workflow jobs", ErrWorkflowJobValidation)
		}
		runner := s.workflowRunner
		if runner == nil {
			return fmt.Errorf("workflow scheduler dispatch is not configured")
		}
		wf, err := runner.ValidateScheduledWorkflow(ctx, WorkflowValidateRequest{UserID: userID, AgentID: agentID, WorkflowID: workflowID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: workflow %q", ErrWorkflowJobNotFound, workflowID)
			}
			return fmt.Errorf("validate workflow: %w", err)
		}
		if wf.ID == "" || wf.ID != workflowID {
			return fmt.Errorf("%w: workflow %q", ErrWorkflowJobNotFound, workflowID)
		}
		if !wf.FullyFrozen && !payloadBool(payload, "allow_replan") {
			return fmt.Errorf("%w: workflow %q is partially frozen; set allow_replan to schedule it", ErrWorkflowJobValidation, workflowID)
		}
	case DispatchKindChat:
		// Handler-mode system builtins carry no agent message; they invoke a Go
		// callback directly. All other chat jobs still require the original prompt.
		if message == "" && ownerKind != JobOwnerSystem {
			return fmt.Errorf("message is required")
		}
	default:
		return fmt.Errorf("invalid dispatch_kind %q", dispatchKind)
	}
	return nil
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
		d, err := time.ParseDuration(sched.Every)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", sched.Every, err)
		}
		if d <= 0 {
			return fmt.Errorf("invalid duration %q: must be positive", sched.Every)
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

	// Tear down the live River registration first.
	s.unscheduleJob(id)

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

		s.unscheduleJob(j.ID)
		if err := s.scheduleJob(j); err != nil {
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
	return s.addJobInternal(addJobSpec{Name: name, Message: message, Schedule: sched, SessionMode: sessionMode, AgentID: agentID, OwnerKind: JobOwnerSystem, ExecScope: execScope, DispatchKind: DispatchKindChat, Enabled: true})
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
	runCtx := withRunOutputSink(WithRunID(WithRunSessionID(ctx, sessionID), runID), outputSink)

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

	// Finalize run bookkeeping on a context detached from cancellation: when a
	// graceful shutdown cancels ctx mid-dispatch, the run row must still move out
	// of "running" so it neither stays stuck nor blocks the next fire.
	bookkeepingCtx := context.WithoutCancel(ctx)
	if err := s.finishJobRun(bookkeepingCtx, runID, job.ID, status, finishedAt, errStr, outputSink.get()); err != nil {
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

	if err := s.recordJobRun(bookkeepingCtx, job.ID, finishedAt, runErr); err != nil {
		s.log.Warn("failed to record scheduler job run", "id", job.ID, "error", err)
	}

	if isOneTime {
		go s.retireOneTimeJob(job.ID)
	}
}

// RunJobNow triggers an immediate execution of the given job asynchronously.
// Returns the run ID of the newly created run record.
// Returns errJobAlreadyRunning (wrapped) if a run is already active for the job.
//
// This is BEST-EFFORT and NON-DURABLE, unlike the cron-scheduled path which runs
// on River: the execution happens in a goroutine on THIS process, not as a River
// job. A crash or restart before it finishes orphans the in-progress run record
// (the lease reaper / run-status guards clean it up) and the run is NOT retried.
// Use it for user-triggered "run now" actions, not for work that must survive a
// restart — enqueue a River job for that.
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
		runCtx := withRunOutputSink(WithRunID(WithRunSessionID(svcCtx, sessionID), runID), outputSink)
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
		UserID: pgtype.Text{},
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

// retireOneTimeJob disables a one-time job after it fires. The River job has
// already completed (we run from inside its own execution), so the registration
// is just forgotten — no JobCancel.
//
// The row is disabled, not deleted: sched_job_run cascades on job deletion, so
// deleting here would wipe the run record (and its root_goal_id attribution)
// moments after it was written, and "run now" on a fired job would stop
// working. A disabled past-timestamp job can never fire again — startup only
// arms enabled jobs, the River worker skips disabled ones, and re-enabling via
// update is rejected while the timestamp is in the past.
func (s *Service) retireOneTimeJob(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.refs, id)
	job, ok := s.jobs[id]
	if !ok {
		return
	}
	job.Enabled = false
	job.UpdatedAt = time.Now().UTC()
	s.jobs[id] = job
	if err := s.updateJob(s.ctx, job); err != nil {
		s.log.Warn("failed to disable one-time job after execution", "id", id, "error", err)
	} else {
		s.log.Info("one-time job disabled after execution", "id", id)
	}
}

// dispatchJob routes a fired job to its handler-mode callback, the default
// agent OnJob, or an orphan error; then runs every listener. Returns the
// first error seen across primary + listeners.
func (s *Service) dispatchJob(ctx context.Context, job Job) error {
	s.mu.Lock()
	handler := s.runtimeBuiltins[job.Name].Handler
	fn := s.onJob
	workflowRunner := s.workflowRunner
	listeners := append([]OnJobFunc(nil), s.listeners...)
	s.mu.Unlock()

	if normalizeDispatchKind(job.DispatchKind) == DispatchKindWorkflow {
		return s.dispatchWorkflowJob(ctx, job, workflowRunner, listeners)
	}

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

func (s *Service) dispatchWorkflowJob(ctx context.Context, job Job, runner WorkflowRunner, listeners []OnJobFunc) error {
	if runner == nil {
		return fmt.Errorf("workflow scheduler dispatch is not configured")
	}
	workflowID, ok := payloadString(job.Payload, "workflow_id")
	if !ok || workflowID == "" {
		return fmt.Errorf("payload.workflow_id is required for workflow jobs")
	}
	runID := RunIDFromContext(ctx)
	if runID == "" {
		return fmt.Errorf("scheduler run id missing from context")
	}
	latest, err := runner.LatestWorkflowRun(ctx, WorkflowLatestRunRequest{WorkflowID: workflowID})
	if err != nil {
		return fmt.Errorf("get latest workflow run: %w", err)
	}
	idempotencyKey := runID
	resumed := false
	if latest.Found {
		switch latest.Status {
		case "claimed", "materializing":
			idempotencyKey = latest.IdempotencyKey
			resumed = true
		case "done":
			if latest.RootGoalID != "" && !latest.RootGoalTerminal {
				msg := "skipped: previous workflow run still active"
				if sink := RunOutputSinkFromContext(ctx); sink != nil {
					sink.Set(msg)
				}
				for _, listener := range listeners {
					if err := listener(ctx, job); err != nil {
						return err
					}
				}
				return nil
			}
		}
	}
	result, err := runner.InstantiateWorkflow(ctx, WorkflowInstantiateRequest{UserID: job.UserID, AgentID: job.AgentID, WorkflowID: workflowID, Inputs: payloadStringMap(job.Payload, "inputs"), IdempotencyKey: idempotencyKey})
	if err != nil {
		return err
	}
	if result.RootGoalID != "" {
		if err := s.q.SetSchedJobRunRootGoal(ctx, sqlc.SetSchedJobRunRootGoalParams{RootGoalID: pgtype.Text{String: result.RootGoalID, Valid: true}, ID: runID, JobID: job.ID}); err != nil {
			return fmt.Errorf("set scheduler run root goal: %w", err)
		}
	}
	if sink := RunOutputSinkFromContext(ctx); sink != nil {
		if resumed {
			sink.Set(fmt.Sprintf("resumed stalled workflow run %s -> goal %s", result.RunID, result.RootGoalID))
		} else {
			sink.Set(fmt.Sprintf("workflow run %s -> goal %s", result.RunID, result.RootGoalID))
		}
	}
	var runErr error
	for _, listener := range listeners {
		if err := listener(ctx, job); err != nil && runErr == nil {
			runErr = err
		}
	}
	return runErr
}
