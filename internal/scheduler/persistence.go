package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func (s *Service) guardedMutation(ctx context.Context, mutate func(*sqlc.Queries) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := agentrun.ValidateTx(ctx, tx); err != nil {
		return err
	}
	if err := mutate(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

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
	return s.guardedMutation(ctx, func(q *sqlc.Queries) error {
		_, err := q.CreateSchedulerJob(ctx, createSchedulerJobParams(job))
		return err
	})
}

// updateJob persists an existing job to the database.
func (s *Service) updateJob(ctx context.Context, job Job) error {
	return s.guardedMutation(ctx, func(q *sqlc.Queries) error { return q.UpdateSchedulerJob(ctx, updateSchedulerJobParams(job)) })
}

// recordJobRun persists execution metadata for a job.
func (s *Service) recordJobRun(ctx context.Context, id string, ranAt time.Time, runErr error) error {
	lastError := ""
	if runErr != nil {
		lastError = runErr.Error()
	}
	return s.guardedMutation(ctx, func(q *sqlc.Queries) error {
		return q.RecordSchedulerJobRun(ctx, sqlc.RecordSchedulerJobRunParams{
			LastRunAt: pgtype.Timestamptz{Time: ranAt.UTC(), Valid: true},
			LastError: lastError,
			UpdatedAt: ranAt.UTC(),
			ID:        id,
		})
	})
}

// deleteJob removes a job from the database.
func (s *Service) deleteJob(ctx context.Context, id string) error {
	return s.guardedMutation(ctx, func(q *sqlc.Queries) error { return q.DeleteSchedulerJob(ctx, id) })
}

func createSchedulerJobParams(job Job) sqlc.CreateSchedulerJobParams {
	createdAt := job.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := job.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	return sqlc.CreateSchedulerJobParams{
		ID:             job.ID,
		OwnerKind:      normalizeOwnerKind(job.OwnerKind),
		ExecScope:      normalizeExecScope(job.ExecScope),
		PluginID:       job.PluginID,
		JobKey:         job.JobKey,
		RuntimeName:    job.RuntimeName,
		Name:           job.Name,
		Description:    job.Description,
		ScheduleCron:   job.Schedule.Cron,
		ScheduleEvery:  job.Schedule.Every,
		ScheduleAt:     job.Schedule.At,
		Message:        job.Message,
		Payload:        encodePayload(job.Payload),
		DispatchKind:   normalizeDispatchKind(job.DispatchKind),
		SessionMode:    job.SessionMode,
		Enabled:        job.Enabled,
		AgentID:        pgtype.Text{String: job.AgentID, Valid: job.AgentID != ""},
		UserID:         pgtype.Text{String: job.UserID, Valid: job.UserID != ""},
		CreatedAt:      createdAt.UTC(),
		UpdatedAt:      updatedAt.UTC(),
		LastRunAt:      nullableTime(job.LastRunAt),
		LastError:      job.LastError,
		IdempotencyKey: pgtype.Text{String: job.IdempotencyKey, Valid: job.IdempotencyKey != ""},
	}
}

func updateSchedulerJobParams(job Job) sqlc.UpdateSchedulerJobParams {
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
		DispatchKind:  normalizeDispatchKind(job.DispatchKind),
		SessionMode:   job.SessionMode,
		Enabled:       job.Enabled,
		AgentID:       pgtype.Text{String: job.AgentID, Valid: job.AgentID != ""},
		UserID:        pgtype.Text{String: job.UserID, Valid: job.UserID != ""},
		UpdatedAt:     updatedAt.UTC(),
		LastRunAt:     nullableTime(job.LastRunAt),
		LastError:     job.LastError,
		ID:            job.ID,
	}
}

func dbRowToJob(r sqlc.SchedJob) Job {
	createdAt := r.CreatedAt.UTC()
	updatedAt := r.UpdatedAt.UTC()
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
		Message:      r.Message,
		Payload:      decodePayload(r.Payload),
		DispatchKind: normalizeDispatchKind(r.DispatchKind),
		SessionMode:  r.SessionMode,
		Enabled:      r.Enabled,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
		LastError:    r.LastError,
	}
	if r.AgentID.Valid {
		j.AgentID = r.AgentID.String
	}
	if r.UserID.Valid {
		j.UserID = r.UserID.String
	}
	if r.IdempotencyKey.Valid {
		j.IdempotencyKey = r.IdempotencyKey.String
	}
	if r.LastRunAt.Valid {
		t := r.LastRunAt.Time.UTC()
		j.LastRunAt = &t
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

func normalizeDispatchKind(kind string) string {
	switch kind {
	case DispatchKindWorkflow:
		return DispatchKindWorkflow
	default:
		return DispatchKindChat
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

func encodePayload(payload map[string]any) json.RawMessage {
	if len(payload) == 0 {
		return json.RawMessage("{}")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage("{}")
	}
	return data
}

func decodePayload(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return map[string]any{}
	}
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

func payloadString(payload map[string]any, key string) (string, bool) {
	v, ok := payload[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func payloadBool(payload map[string]any, key string) bool {
	v, ok := payload[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

func payloadStringMap(payload map[string]any, key string) map[string]string {
	v, ok := payload[key]
	if !ok {
		return map[string]string{}
	}
	if m, ok := v.(map[string]string); ok {
		return m
	}
	if m, ok := v.(map[string]any); ok {
		out := make(map[string]string, len(m))
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
		return out
	}
	return map[string]string{}
}

func clonePayload(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	maps.Copy(out, src)
	return out
}

func nullableTime(t *time.Time) pgtype.Timestamptz {
	if t == nil || t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

// tryStartJobRun ensures at most one run is in progress for the job and creates
// the initial "running" record. The COUNT-then-INSERT is not atomic under
// Postgres Read Committed, and River runs the same job concurrently on up to
// schedulerMaxWorkers workers per node and across nodes, so two fires could each
// observe zero running rows and double-execute. A transaction-scoped advisory
// lock keyed on the job ID serializes these fires: the second blocks until the
// first commits, then sees the running row and bails. Returns errJobAlreadyRunning
// if a run is already active.
func (s *Service) tryStartJobRun(ctx context.Context, id, jobID, sessionID string, userID string, startedAt time.Time) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)
	if err := qtx.LockSchedJobForRun(ctx, jobID); err != nil {
		return fmt.Errorf("lock job: %w", err)
	}
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
		StartedAt: startedAt.UTC(),
		UserID:    pgtype.Text{String: userID, Valid: userID != ""},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
	return s.guardedMutation(ctx, func(q *sqlc.Queries) error {
		return q.UpdateSchedJobRun(ctx, sqlc.UpdateSchedJobRunParams{
			Status:     status,
			FinishedAt: pgtype.Timestamptz{Time: finishedAt.UTC(), Valid: true},
			Error:      errStr,
			Output:     truncateRunes(output, maxRunOutputLen),
			ID:         id,
			JobID:      jobID,
		})
	})
}

func dbRowToJobRun(r sqlc.SchedJobRun) JobRun {
	startedAt := r.StartedAt.UTC()
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
		t := r.FinishedAt.Time.UTC()
		run.FinishedAt = &t
	}
	return run
}
