package admin

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/vaayne/anna/internal/scheduler"
	"github.com/vaayne/anna/pkg/db/sqlc"
	"github.com/vaayne/anna/pkg/memory"
)

const adminDBTimeLayout = "2006-01-02 15:04:05"

// schedulerJobJSON is the JSON representation for scheduler jobs.
type schedulerJobJSON struct {
	ID          string         `json:"id"`
	OwnerKind   string         `json:"owner_kind,omitempty"`
	ExecScope   string         `json:"exec_scope,omitempty"`
	PluginID    string         `json:"plugin_id,omitempty"`
	JobKey      string         `json:"job_key,omitempty"`
	RuntimeName string         `json:"runtime_name,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Cron        string         `json:"cron,omitempty"`
	Every       string         `json:"every,omitempty"`
	At          string         `json:"at,omitempty"`
	Message     string         `json:"message,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	SessionMode string         `json:"session_mode"`
	Enabled     bool           `json:"enabled"`
	AgentID     string         `json:"agent_id,omitempty"`
	UserID      int64          `json:"user_id,omitempty"`
	CreatedAt   string         `json:"created_at,omitempty"`
	UpdatedAt   string         `json:"updated_at,omitempty"`
	LastRunAt   string         `json:"last_run_at,omitempty"`
	LastError   string         `json:"last_error,omitempty"`
}

func (s *Server) ListSchedulerJobs(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())

	rows, err := s.q.ListSchedulerJobs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jobs := make([]schedulerJobJSON, 0, len(rows))
	for _, row := range rows {
		j := dbRowToJobJSON(row)
		// Non-admin users only see their own user-owned jobs.
		if info != nil && !info.IsAdmin {
			if j.OwnerKind == scheduler.JobOwnerPlugin || j.UserID != info.UserID {
				continue
			}
		}
		jobs = append(jobs, j)
	}
	writeData(w, http.StatusOK, jobs)
}

func (s *Server) CreateSchedulerJob(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())

	var body schedulerJobJSON
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Name == "" || body.Message == "" {
		writeError(w, http.StatusBadRequest, "name and message are required")
		return
	}
	if err := validateSchedule(body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.SessionMode == "" {
		body.SessionMode = "reuse"
	}

	// Non-admin users always own their jobs; only admins can create system jobs (user_id=0).
	if info != nil && !info.IsAdmin {
		body.UserID = info.UserID
	}

	if s.schedulerSvc != nil {
		sched := scheduler.Schedule{Cron: body.Cron, Every: body.Every, At: body.At}
		job, err := s.schedulerSvc.AddJobWithOwner(body.Name, body.Message, sched, body.SessionMode, body.AgentID, body.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		body.ID = job.ID
		body.Enabled = job.Enabled
		writeData(w, http.StatusCreated, body)
		return
	}

	// Infer exec_scope from user_id when not explicitly set.
	execScope := body.ExecScope
	if execScope == "" {
		if body.UserID > 0 {
			execScope = scheduler.ExecScopeUser
		} else {
			execScope = scheduler.ExecScopeSystem
		}
	}

	id := generateShortID()
	enabled := int64(0)
	if body.Enabled {
		enabled = 1
	}

	now := time.Now().UTC().Format(adminDBTimeLayout)
	_, err := s.q.CreateSchedulerJob(r.Context(), sqlc.CreateSchedulerJobParams{
		ID:            id,
		OwnerKind:     scheduler.JobOwnerUser,
		ExecScope:     execScope,
		PluginID:      "",
		JobKey:        "",
		RuntimeName:   "",
		Name:          body.Name,
		Description:   body.Description,
		ScheduleCron:  body.Cron,
		ScheduleEvery: body.Every,
		ScheduleAt:    body.At,
		Message:       body.Message,
		Payload:       "{}",
		SessionMode:   body.SessionMode,
		Enabled:       enabled,
		AgentID:       sql.NullString{String: body.AgentID, Valid: body.AgentID != ""},
		UserID:        sql.NullInt64{Int64: body.UserID, Valid: body.UserID != 0},
		CreatedAt:     now,
		UpdatedAt:     now,
		LastRunAt:     sql.NullString{},
		LastError:     "",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	body.ID = id
	writeData(w, http.StatusCreated, body)
}

func (s *Server) UpdateSchedulerJob(w http.ResponseWriter, r *http.Request, id string) {
	info := UserFromContext(r.Context())

	existing, err := s.q.GetSchedulerJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if existing.OwnerKind == scheduler.JobOwnerPlugin {
		writeError(w, http.StatusBadRequest, "plugin-owned jobs are read-only in admin")
		return
	}

	// Non-admin users can only update their own jobs.
	if info != nil && !info.IsAdmin {
		if !existing.UserID.Valid || existing.UserID.Int64 != info.UserID {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
	}

	var body schedulerJobJSON
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Merge: use existing values for empty fields.
	if body.Name == "" {
		body.Name = existing.Name
	}
	if body.Message == "" {
		body.Message = existing.Message
	}
	if body.Cron == "" && body.Every == "" && body.At == "" {
		body.Cron = existing.ScheduleCron
		body.Every = existing.ScheduleEvery
		body.At = existing.ScheduleAt
	}
	if body.SessionMode == "" {
		body.SessionMode = existing.SessionMode
	}

	// Non-admin users cannot change ownership.
	if info != nil && !info.IsAdmin {
		body.UserID = info.UserID
	}

	enabled := int64(0)
	if body.Enabled {
		enabled = 1
	}

	err = s.q.UpdateSchedulerJob(r.Context(), sqlc.UpdateSchedulerJobParams{
		ID:            id,
		OwnerKind:     existing.OwnerKind,
		ExecScope:     existing.ExecScope,
		PluginID:      existing.PluginID,
		JobKey:        existing.JobKey,
		RuntimeName:   existing.RuntimeName,
		Name:          body.Name,
		Description:   existing.Description,
		ScheduleCron:  body.Cron,
		ScheduleEvery: body.Every,
		ScheduleAt:    body.At,
		Message:       body.Message,
		Payload:       existing.Payload,
		SessionMode:   body.SessionMode,
		Enabled:       enabled,
		AgentID:       sql.NullString{String: body.AgentID, Valid: body.AgentID != ""},
		UserID:        sql.NullInt64{Int64: body.UserID, Valid: body.UserID != 0},
		UpdatedAt:     time.Now().UTC().Format(adminDBTimeLayout),
		LastRunAt:     existing.LastRunAt,
		LastError:     existing.LastError,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	body.ID = id
	writeData(w, http.StatusOK, body)
}

func (s *Server) DeleteSchedulerJob(w http.ResponseWriter, r *http.Request, id string) {
	info := UserFromContext(r.Context())

	existing, err := s.q.GetSchedulerJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if existing.OwnerKind == scheduler.JobOwnerPlugin {
		writeError(w, http.StatusBadRequest, "plugin-owned jobs are read-only in admin")
		return
	}

	// Non-admin users can only delete their own jobs.
	if info != nil && !info.IsAdmin {
		if !existing.UserID.Valid || existing.UserID.Int64 != info.UserID {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
	}

	var deleteErr error
	if s.schedulerSvc != nil {
		deleteErr = s.schedulerSvc.RemoveJob(id)
	} else {
		deleteErr = s.q.DeleteSchedulerJob(r.Context(), id)
	}
	if deleteErr != nil {
		writeError(w, http.StatusInternalServerError, deleteErr.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func dbRowToJobJSON(row sqlc.SchedJob) schedulerJobJSON {
	j := schedulerJobJSON{
		ID:          row.ID,
		OwnerKind:   row.OwnerKind,
		ExecScope:   row.ExecScope,
		PluginID:    row.PluginID,
		JobKey:      row.JobKey,
		RuntimeName: row.RuntimeName,
		Name:        row.Name,
		Description: row.Description,
		Cron:        row.ScheduleCron,
		Every:       row.ScheduleEvery,
		At:          row.ScheduleAt,
		Message:     row.Message,
		SessionMode: row.SessionMode,
		Enabled:     row.Enabled != 0,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		LastError:   row.LastError,
	}
	if payload := decodeSchedulerPayload(row.Payload); len(payload) > 0 {
		j.Payload = payload
	}
	if row.AgentID.Valid {
		j.AgentID = row.AgentID.String
	}
	if row.UserID.Valid {
		j.UserID = row.UserID.Int64
	}
	if row.LastRunAt.Valid {
		j.LastRunAt = row.LastRunAt.String
	}
	return j
}

func decodeSchedulerPayload(raw string) map[string]any {
	if raw == "" || raw == "{}" {
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

func validateSchedule(body schedulerJobJSON) error {
	count := 0
	if body.Cron != "" {
		count++
	}
	if body.Every != "" {
		count++
	}
	if body.At != "" {
		count++
	}
	if count == 0 {
		return fmt.Errorf("schedule requires one of: cron, every, or at")
	}
	if count > 1 {
		return fmt.Errorf("schedule must have exactly one of: cron, every, or at")
	}
	if body.Every != "" {
		if _, err := time.ParseDuration(body.Every); err != nil {
			return fmt.Errorf("invalid duration %q: %w", body.Every, err)
		}
	}
	return nil
}

func generateShortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type jobRunJSON struct {
	ID         string `json:"id"`
	JobID      string `json:"job_id"`
	SessionID  string `json:"session_id"`
	UserID     int64  `json:"user_id,omitempty"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Error      string `json:"error,omitempty"`
	Duration   string `json:"duration,omitempty"`
}

func (s *Server) triggerSchedulerJob(w http.ResponseWriter, r *http.Request) {
	if s.schedulerSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}

	id := r.PathValue("id")

	existing, err := s.q.GetSchedulerJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	info := UserFromContext(r.Context())
	if info != nil && !info.IsAdmin {
		if existing.OwnerKind == scheduler.JobOwnerPlugin {
			writeError(w, http.StatusForbidden, "cannot manually trigger plugin jobs")
			return
		}
		if !existing.UserID.Valid || existing.UserID.Int64 != info.UserID {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
	}

	runID, err := s.schedulerSvc.RunJobNow(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	resp := map[string]string{"status": "triggered"}
	if runID != "" {
		resp["run_id"] = runID
	}
	writeData(w, http.StatusAccepted, resp)
}

func (s *Server) listSchedulerJobRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	existing, err := s.q.GetSchedulerJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	info := UserFromContext(r.Context())
	if info != nil && !info.IsAdmin {
		// Non-admin: block plugin jobs, system jobs (no user_id), and other users' jobs.
		if existing.OwnerKind == scheduler.JobOwnerPlugin ||
			existing.OwnerKind == scheduler.JobOwnerSystem ||
			!existing.UserID.Valid ||
			existing.UserID.Int64 != info.UserID {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
	}

	rows, err := s.q.ListSchedJobRuns(r.Context(), sqlc.ListSchedJobRunsParams{
		JobID: id,
		Limit: 20,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	sm, _ := s.mem.(memory.SessionManager)
	result := make([]jobRunJSON, 0, len(rows))
	for _, row := range rows {
		j := dbRowToJobRunJSON(row)
		if j.SessionID != "" && sm != nil {
			if _, err := sm.LoadInfo(r.Context(), j.SessionID); err != nil {
				j.SessionID = ""
			}
		}
		result = append(result, j)
	}
	writeData(w, http.StatusOK, result)
}

func dbRowToJobRunJSON(row sqlc.SchedJobRun) jobRunJSON {
	j := jobRunJSON{
		ID:        row.ID,
		JobID:     row.JobID,
		SessionID: row.SessionID,
		Status:    row.Status,
		StartedAt: row.StartedAt,
		Error:     row.Error,
	}
	if row.UserID.Valid {
		j.UserID = row.UserID.Int64
	}
	if row.FinishedAt.Valid {
		j.FinishedAt = row.FinishedAt.String
		startedAt, err1 := time.Parse(adminDBTimeLayout, row.StartedAt)
		finishedAt, err2 := time.Parse(adminDBTimeLayout, row.FinishedAt.String)
		if err1 == nil && err2 == nil {
			j.Duration = finishedAt.Sub(startedAt).Truncate(time.Second).String()
		}
	}
	return j
}
