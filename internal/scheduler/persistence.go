package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// dbTimeLayout is the SQLite datetime format used for all time columns.
const dbTimeLayout = "2006-01-02 15:04:05"

// loadJobs reads all persisted jobs from the database.
func (s *Service) loadJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.q.ListAllSchedulerJobs(ctx)
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
	_, err := s.q.CreateSchedulerJob(ctx, createSchedulerJobParams(job))
	return err
}

// updateJob persists an existing job to the database.
func (s *Service) updateJob(ctx context.Context, job Job) error {
	return s.q.UpdateSchedulerJob(ctx, updateSchedulerJobParams(job))
}

// recordJobRun persists execution metadata for a job.
func (s *Service) recordJobRun(ctx context.Context, id string, ranAt time.Time, runErr error) error {
	lastError := ""
	if runErr != nil {
		lastError = runErr.Error()
	}
	return s.q.RecordSchedulerJobRun(ctx, sqlc.RecordSchedulerJobRunParams{
		LastRunAt: sql.NullString{String: ranAt.UTC().Format(dbTimeLayout), Valid: true},
		LastError: lastError,
		UpdatedAt: ranAt.UTC().Format(dbTimeLayout),
		ID:        id,
	})
}

// deleteJob removes a job from the database.
func (s *Service) deleteJob(ctx context.Context, id string) error {
	return s.q.DeleteSchedulerJob(ctx, id)
}

func createSchedulerJobParams(job Job) sqlc.CreateSchedulerJobParams {
	enabled := int64(0)
	if job.Enabled {
		enabled = 1
	}
	createdAt := job.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := job.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	return sqlc.CreateSchedulerJobParams{
		ID:            job.ID,
		OwnerKind:     normalizeOwnerKind(job.OwnerKind),
		ExecScope:     normalizeExecScope(job.ExecScope),
		PluginID:      job.PluginID,
		JobKey:        job.JobKey,
		RuntimeName:   job.RuntimeName,
		Name:          job.Name,
		Description:   job.Description,
		ScheduleCron:  job.Schedule.Cron,
		ScheduleEvery: job.Schedule.Every,
		ScheduleAt:    job.Schedule.At,
		Message:       job.Message,
		Payload:       encodePayload(job.Payload),
		SessionMode:   job.SessionMode,
		Enabled:       enabled,
		AgentID:       sql.NullString{String: job.AgentID, Valid: job.AgentID != ""},
		UserID:        sql.NullString{String: job.UserID, Valid: job.UserID != ""},
		CreatedAt:     createdAt.UTC().Format(dbTimeLayout),
		UpdatedAt:     updatedAt.UTC().Format(dbTimeLayout),
		LastRunAt:     nullableTime(job.LastRunAt),
		LastError:     job.LastError,
	}
}

func updateSchedulerJobParams(job Job) sqlc.UpdateSchedulerJobParams {
	enabled := int64(0)
	if job.Enabled {
		enabled = 1
	}
	updatedAt := job.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	return sqlc.UpdateSchedulerJobParams{
		OwnerKind:     normalizeOwnerKind(job.OwnerKind),
		ExecScope:     normalizeExecScope(job.ExecScope),
		PluginID:      job.PluginID,
		JobKey:        job.JobKey,
		RuntimeName:   job.RuntimeName,
		Name:          job.Name,
		Description:   job.Description,
		ScheduleCron:  job.Schedule.Cron,
		ScheduleEvery: job.Schedule.Every,
		ScheduleAt:    job.Schedule.At,
		Message:       job.Message,
		Payload:       encodePayload(job.Payload),
		SessionMode:   job.SessionMode,
		Enabled:       enabled,
		AgentID:       sql.NullString{String: job.AgentID, Valid: job.AgentID != ""},
		UserID:        sql.NullString{String: job.UserID, Valid: job.UserID != ""},
		UpdatedAt:     updatedAt.UTC().Format(dbTimeLayout),
		LastRunAt:     nullableTime(job.LastRunAt),
		LastError:     job.LastError,
		ID:            job.ID,
	}
}

func dbRowToJob(r sqlc.SchedJob) Job {
	createdAt, _ := time.Parse(dbTimeLayout, r.CreatedAt)
	updatedAt, _ := time.Parse(dbTimeLayout, r.UpdatedAt)
	j := Job{
		ID:          r.ID,
		OwnerKind:   normalizeOwnerKind(r.OwnerKind),
		ExecScope:   normalizeExecScope(r.ExecScope),
		PluginID:    r.PluginID,
		JobKey:      r.JobKey,
		RuntimeName: r.RuntimeName,
		Name:        r.Name,
		Description: r.Description,
		Schedule: Schedule{
			Cron:  r.ScheduleCron,
			Every: r.ScheduleEvery,
			At:    r.ScheduleAt,
		},
		Message:     r.Message,
		Payload:     decodePayload(r.Payload),
		SessionMode: r.SessionMode,
		Enabled:     r.Enabled != 0,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		LastError:   r.LastError,
	}
	if r.AgentID.Valid {
		j.AgentID = r.AgentID.String
	}
	if r.UserID.Valid {
		j.UserID = r.UserID.String
	}
	if r.LastRunAt.Valid {
		if parsed, err := time.Parse(dbTimeLayout, r.LastRunAt.String); err == nil {
			j.LastRunAt = &parsed
		}
	}
	return j
}

func normalizeOwnerKind(kind string) string {
	switch kind {
	case JobOwnerPlugin:
		return JobOwnerPlugin
	case JobOwnerSystem:
		return JobOwnerSystem
	default:
		return JobOwnerUser
	}
}

func normalizeExecScope(scope string) string {
	switch scope {
	case ExecScopeSystem:
		return ExecScopeSystem
	case ExecScopeUser:
		return ExecScopeUser
	default:
		slog.Default().Warn("unknown exec_scope, defaulting to user", "scope", scope)
		return ExecScopeUser
	}
}

func encodePayload(payload map[string]any) string {
	if len(payload) == 0 {
		return "{}"
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func decodePayload(raw string) map[string]any {
	if raw == "" {
		return map[string]any{}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return map[string]any{}
	}
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

func clonePayload(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	maps.Copy(out, src)
	return out
}

func nullableTime(t *time.Time) sql.NullString {
	if t == nil || t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(dbTimeLayout), Valid: true}
}

// tryStartJobRun atomically checks that no run is already in progress for the
// job and creates the initial "running" record in a single transaction.
// SQLite serializes writes, so Begin + check + insert is effectively atomic.
// Returns errJobAlreadyRunning if a run is already active.
func (s *Service) tryStartJobRun(ctx context.Context, id, jobID, sessionID string, userID string, startedAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := s.q.WithTx(tx)
	count, err := qtx.CountRunningSchedJobRuns(ctx, jobID)
	if err != nil {
		return fmt.Errorf("check running: %w", err)
	}
	if count > 0 {
		return errJobAlreadyRunning
	}
	if _, err := qtx.CreateSchedJobRun(ctx, sqlc.CreateSchedJobRunParams{
		ID:        id,
		JobID:     jobID,
		SessionID: sessionID,
		Status:    RunStatusRunning,
		StartedAt: startedAt.UTC().Format(dbTimeLayout),
		UserID:    sql.NullString{String: userID, Valid: userID != ""},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// maxRunOutputLen caps stored run output so run rows stay cheap to list;
// the full transcript lives in the run's session.
const maxRunOutputLen = 4000

func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

func (s *Service) finishJobRun(ctx context.Context, id, jobID, status string, finishedAt time.Time, errStr, output string) error {
	return s.q.UpdateSchedJobRun(ctx, sqlc.UpdateSchedJobRunParams{
		Status:     status,
		FinishedAt: sql.NullString{String: finishedAt.UTC().Format(dbTimeLayout), Valid: true},
		Error:      errStr,
		Output:     truncateRunes(output, maxRunOutputLen),
		ID:         id,
		JobID:      jobID,
	})
}

func dbRowToJobRun(r sqlc.SchedJobRun) JobRun {
	startedAt, _ := time.Parse(dbTimeLayout, r.StartedAt)
	run := JobRun{
		ID:        r.ID,
		JobID:     r.JobID,
		SessionID: r.SessionID,
		Status:    r.Status,
		StartedAt: startedAt,
		Error:     r.Error,
		Output:    r.Output,
	}
	if r.UserID.Valid {
		run.UserID = r.UserID.String
	}
	if r.FinishedAt.Valid {
		if parsed, err := time.Parse(dbTimeLayout, r.FinishedAt.String); err == nil {
			run.FinishedAt = &parsed
		}
	}
	return run
}
