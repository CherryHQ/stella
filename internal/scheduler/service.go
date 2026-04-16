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
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/notify"
	"github.com/vaayne/anna/pkg/db/sqlc"
	"github.com/vaayne/anna/pkg/memory"
)

// errOneTimeJobPast is returned by scheduleJob when a one-time job's timestamp
// has already elapsed. Start suppresses this for persisted jobs; AddJob treats
// it as a hard failure.
var errOneTimeJobPast = errors.New("one-time job timestamp is in the past")

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
	dataPath        string          // legacy data dir for jobs.json migration
	ctx             context.Context // lifecycle context from Start
	mu              sync.Mutex
	jobs            map[string]Job
	gids            map[string]uuid.UUID // job ID -> gocron job UUID
	log             *slog.Logger
	userJobsEnabled bool

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

// SetLegacyDataPath sets the directory where the legacy jobs.json file may
// exist. If set, Start will attempt a one-time migration from file to DB.
func (s *Service) SetLegacyDataPath(path string) {
	s.dataPath = path
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
// Use this when the scheduler is only needed for internal tasks such as heartbeat.
func (s *Service) StartEphemeral(ctx context.Context) error {
	return s.start(ctx, false)
}

func (s *Service) start(ctx context.Context, loadPersisted bool) error {
	if loadPersisted && s.dataPath != "" {
		if err := s.migrateJobsFile(ctx, s.dataPath); err != nil {
			s.log.Warn("failed to migrate legacy jobs.json", "error", err)
		}
	}
	if loadPersisted {
		if err := s.migrateLegacyPluginJobs(ctx); err != nil {
			return fmt.Errorf("migrate legacy plugin jobs: %w", err)
		}
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
	if loadPersisted {
		for _, j := range jobs {
			s.jobs[j.ID] = j
			if j.OwnerKind == JobOwnerUser && !s.userJobsEnabled {
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

// AddJob creates, persists, and schedules a new job.
// sessionMode controls session reuse: "reuse" (default) or "new".
func (s *Service) AddJob(name, message string, sched Schedule, sessionMode string) (Job, error) {
	return s.addJobWithOwner(name, message, sched, sessionMode, "", 0)
}

// AddJobForContext creates a user-owned job bound to the current execution context.
// When the caller context carries agent/user scope, scheduled executions inherit it.
func (s *Service) AddJobForContext(ctx context.Context, name, message string, sched Schedule, sessionMode string) (Job, error) {
	return s.addJobWithOwner(
		name,
		message,
		sched,
		sessionMode,
		memory.AgentIDFromContext(ctx),
		memory.UserIDFromContext(ctx),
	)
}

func (s *Service) addJobWithOwner(name, message string, sched Schedule, sessionMode, agentID string, userID int64) (Job, error) {
	if name == "" {
		return Job{}, fmt.Errorf("name is required")
	}
	if message == "" {
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
		OwnerKind:   JobOwnerUser,
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

	s.log.Info("job added", "id", job.ID, "name", name, "agent_id", agentID, "user_id", userID)
	return job, nil
}

// AddPluginJob creates, persists, and schedules a plugin-owned job.
func (s *Service) AddPluginJob(pluginID, key, runtimeName, name, description string, sched Schedule, payload map[string]any) (Job, error) {
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
		s.mu.Lock()
		fn := s.onJob
		listeners := append([]OnJobFunc(nil), s.listeners...)
		s.mu.Unlock()
		var runErr error
		if fn != nil {
			if err := fn(ctx, captured); err != nil {
				runErr = err
			}
		}
		for _, listener := range listeners {
			if err := listener(ctx, captured); err != nil && runErr == nil {
				runErr = err
			}
		}
		ranAt := time.Now().UTC()
		s.mu.Lock()
		if jobState, ok := s.jobs[captured.ID]; ok {
			jobState.LastRunAt = &ranAt
			if runErr != nil {
				jobState.LastError = runErr.Error()
			} else {
				jobState.LastError = ""
			}
			jobState.UpdatedAt = ranAt
			s.jobs[captured.ID] = jobState
		}
		s.mu.Unlock()
		if err := s.recordJobRun(ctx, captured.ID, ranAt, runErr); err != nil {
			s.log.Warn("failed to record scheduler job run", "id", captured.ID, "error", err)
		}
		if isOneTime {
			go s.removeOneTimeJob(captured.ID)
		}
	}))
	if err != nil {
		return err
	}

	s.gids[job.ID] = gj.ID()
	return nil
}

// removeOneTimeJob cleans up a one-time job after it fires.
func (s *Service) removeOneTimeJob(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if gid, ok := s.gids[id]; ok {
		_ = s.scheduler.RemoveJob(gid)
		delete(s.gids, id)
	}
	delete(s.jobs, id)
	if err := s.deleteJob(s.ctx, id); err != nil {
		s.log.Warn("failed to remove one-time job after execution", "id", id, "error", err)
	} else {
		s.log.Info("one-time job auto-removed after execution", "id", id)
	}
}
