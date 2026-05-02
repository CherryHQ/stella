package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/vaayne/anna/pkg/db/sqlc"
)

// Store provides DB-backed scheduler operations for external callers such as
// CLI commands. Unlike Service, it writes only to the database; the running
// Service picks up changes on its next start.
type Store struct {
	q *sqlc.Queries
}

// NewStore creates a Store backed by the given database.
func NewStore(db *sql.DB) *Store {
	return &Store{q: sqlc.New(db)}
}

// AddJobParams holds parameters for creating a new user job via the Store.
type AddJobParams struct {
	Name        string
	Message     string
	Schedule    Schedule
	SessionMode string
	UserID      int64
	AgentID     string
}

// AddJob validates params, creates the job in the database, and returns it.
func (s *Store) AddJob(ctx context.Context, p AddJobParams) (Job, error) {
	if p.Name == "" {
		return Job{}, fmt.Errorf("name is required")
	}
	if p.Message == "" {
		return Job{}, fmt.Errorf("message is required")
	}
	if err := validateSchedule(p.Schedule); err != nil {
		return Job{}, err
	}
	if p.SessionMode == "" {
		p.SessionMode = SessionReuse
	}
	if p.SessionMode != SessionReuse && p.SessionMode != SessionNew {
		return Job{}, fmt.Errorf("invalid session_mode %q: must be %q or %q", p.SessionMode, SessionReuse, SessionNew)
	}

	now := time.Now().UTC()
	job := Job{
		ID:          newShortID(),
		OwnerKind:   JobOwnerUser,
		Name:        p.Name,
		Message:     p.Message,
		Schedule:    p.Schedule,
		SessionMode: p.SessionMode,
		Enabled:     true,
		AgentID:     p.AgentID,
		UserID:      p.UserID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if _, err := s.q.CreateSchedulerJob(ctx, createSchedulerJobParams(job)); err != nil {
		return Job{}, fmt.Errorf("create job: %w", err)
	}
	return job, nil
}

// ListUserJobs returns all user-owned jobs for the given user, hiding plugin jobs.
func (s *Store) ListUserJobs(ctx context.Context, userID int64) ([]Job, error) {
	rows, err := s.q.ListSchedulerJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	jobs := make([]Job, 0, len(rows))
	for _, r := range rows {
		if r.OwnerKind == JobOwnerPlugin {
			continue
		}
		if r.UserID.Valid && r.UserID.Int64 != userID {
			continue
		}
		jobs = append(jobs, dbRowToJob(r))
	}
	return jobs, nil
}

// RemoveJob deletes a user-owned job by ID, checking ownership.
func (s *Store) RemoveJob(ctx context.Context, id string, userID int64) error {
	row, err := s.q.GetSchedulerJob(ctx, id)
	if err != nil {
		return fmt.Errorf("job %q not found", id)
	}
	if row.OwnerKind == JobOwnerPlugin {
		return fmt.Errorf("job %q is plugin-owned and cannot be removed", id)
	}
	if row.UserID.Valid && row.UserID.Int64 != userID {
		return fmt.Errorf("job %q not found or access denied", id)
	}
	return s.q.DeleteSchedulerJob(ctx, id)
}

func newShortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
