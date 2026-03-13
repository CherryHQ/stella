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
	"github.com/vaayne/anna/internal/channel"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/db/sqlc"
)

// errOneTimeJobPast is returned by scheduleJob when a one-time job's timestamp
// has already elapsed. Start suppresses this for persisted jobs; AddJob treats
// it as a hard failure.
var errOneTimeJobPast = errors.New("one-time job timestamp is in the past")

// OnJobFunc is called when a scheduled job fires.
type OnJobFunc func(ctx context.Context, job Job)

// TaskFunc is a lightweight scheduled callback that is not persisted as a scheduled job.
type TaskFunc func(ctx context.Context)

// Service manages scheduled jobs backed by gocron/v2 with database persistence.
type Service struct {
	scheduler gocron.Scheduler
	onJob     OnJobFunc
	db        *sql.DB
	q         *sqlc.Queries
	ownsDB    bool            // true when Service opened the DB itself
	dataPath  string          // legacy data dir for jobs.json migration
	ctx       context.Context // lifecycle context from Start
	mu        sync.Mutex
	jobs      map[string]Job
	gids      map[string]uuid.UUID // job ID -> gocron job UUID
	log       *slog.Logger

	// Heartbeat (optional, configured via SetHeartbeat).
	heartbeatCfg      *HeartbeatConfig
	heartbeatChat     ChatFunc
	heartbeatNotifier channel.Notifier
}

// New creates a scheduler service backed by the given database.
// Call Start to load persisted jobs and begin scheduling.
func New(db *sql.DB) (*Service, error) {
	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("create scheduler: %w", err)
	}
	return &Service{
		scheduler: s,
		db:        db,
		q:         sqlc.New(db),
		jobs:      make(map[string]Job),
		gids:      make(map[string]uuid.UUID),
		log:       slog.With("component", "scheduler"),
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

// SetOnJob sets the callback invoked when a job fires.
func (s *Service) SetOnJob(fn OnJobFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onJob = fn
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
	if name == "" {
		return Job{}, fmt.Errorf("name is required")
	}
	if message == "" {
		return Job{}, fmt.Errorf("message is required")
	}
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
		return Job{}, fmt.Errorf("schedule requires one of cron, every, or at")
	}
	if setCount > 1 {
		return Job{}, fmt.Errorf("schedule must have exactly one of cron, every, or at")
	}

	// Validate schedule before persisting.
	if sched.Every != "" {
		if _, err := time.ParseDuration(sched.Every); err != nil {
			return Job{}, fmt.Errorf("invalid duration %q: %w", sched.Every, err)
		}
	}
	if sched.At != "" {
		t, err := time.Parse(time.RFC3339, sched.At)
		if err != nil {
			return Job{}, fmt.Errorf("invalid at timestamp %q: must be RFC3339 format: %w", sched.At, err)
		}
		if !t.After(time.Now()) {
			return Job{}, fmt.Errorf("at timestamp %q is in the past", sched.At)
		}
	}

	if sessionMode == "" {
		sessionMode = SessionReuse
	}
	if sessionMode != SessionReuse && sessionMode != SessionNew {
		return Job{}, fmt.Errorf("invalid session_mode %q: must be %q or %q", sessionMode, SessionReuse, SessionNew)
	}

	job := Job{
		ID:          uuid.New().String()[:8],
		Name:        name,
		Schedule:    sched,
		Message:     message,
		SessionMode: sessionMode,
		Enabled:     true,
		CreatedAt:   time.Now(),
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

	s.log.Info("job added", "id", job.ID, "name", name)
	return job, nil
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
		s.mu.Unlock()
		if fn != nil {
			fn(ctx, captured)
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
