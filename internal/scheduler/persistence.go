package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vaayne/anna/internal/db/sqlc"
)

// loadJobs reads all persisted jobs from the database.
func (s *Service) loadJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.q.ListSchedulerJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list scheduler jobs: %w", err)
	}
	jobs := make([]Job, 0, len(rows))
	for _, r := range rows {
		jobs = append(jobs, dbRowToJob(r))
	}
	return jobs, nil
}

// insertJob persists a new job to the database.
func (s *Service) insertJob(ctx context.Context, job Job) error {
	enabled := int64(0)
	if job.Enabled {
		enabled = 1
	}
	_, err := s.q.CreateSchedulerJob(ctx, sqlc.CreateSchedulerJobParams{
		ID:            job.ID,
		Name:          job.Name,
		ScheduleCron:  job.Schedule.Cron,
		ScheduleEvery: job.Schedule.Every,
		ScheduleAt:    job.Schedule.At,
		Message:       job.Message,
		SessionMode:   job.SessionMode,
		Enabled:       enabled,
		AgentID:       sql.NullString{String: job.AgentID, Valid: job.AgentID != ""},
		UserID:        sql.NullInt64{Int64: job.UserID, Valid: job.UserID != 0},
		CreatedAt:     job.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
	})
	return err
}

// deleteJob removes a job from the database.
func (s *Service) deleteJob(ctx context.Context, id string) error {
	return s.q.DeleteSchedulerJob(ctx, id)
}

// migrateJobsFile imports jobs from the legacy jobs.json file into the database
// and removes the file. This is a one-time migration on first startup.
func (s *Service) migrateJobsFile(ctx context.Context, dataPath string) error {
	if dataPath == "" {
		return nil
	}
	file := filepath.Join(dataPath, "jobs.json")
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy jobs.json: %w", err)
	}

	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return fmt.Errorf("parse legacy jobs.json: %w", err)
	}
	if len(jobs) == 0 {
		_ = os.Remove(file)
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := s.q.WithTx(tx)
	for _, job := range jobs {
		enabled := int64(0)
		if job.Enabled {
			enabled = 1
		}
		_, err := qtx.CreateSchedulerJob(ctx, sqlc.CreateSchedulerJobParams{
			ID:            job.ID,
			Name:          job.Name,
			ScheduleCron:  job.Schedule.Cron,
			ScheduleEvery: job.Schedule.Every,
			ScheduleAt:    job.Schedule.At,
			Message:       job.Message,
			SessionMode:   job.SessionMode,
			Enabled:       enabled,
			AgentID:       sql.NullString{String: job.AgentID, Valid: job.AgentID != ""},
			UserID:        sql.NullInt64{Int64: job.UserID, Valid: job.UserID != 0},
			CreatedAt:     job.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
		})
		if err != nil {
			return fmt.Errorf("migrate job %s: %w", job.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}

	_ = os.Remove(file)
	s.log.Info("migrated legacy jobs.json to database", "count", len(jobs))
	return nil
}

func dbRowToJob(r sqlc.SchedulerJob) Job {
	t, _ := time.Parse("2006-01-02 15:04:05", r.CreatedAt)
	j := Job{
		ID:   r.ID,
		Name: r.Name,
		Schedule: Schedule{
			Cron:  r.ScheduleCron,
			Every: r.ScheduleEvery,
			At:    r.ScheduleAt,
		},
		Message:     r.Message,
		SessionMode: r.SessionMode,
		Enabled:     r.Enabled != 0,
		CreatedAt:   t,
	}
	if r.AgentID.Valid {
		j.AgentID = r.AgentID.String
	}
	if r.UserID.Valid {
		j.UserID = r.UserID.Int64
	}
	return j
}
