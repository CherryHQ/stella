package admin

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/vaayne/anna/internal/db/sqlc"
)

// schedulerJobJSON is the JSON representation for scheduler jobs.
type schedulerJobJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Cron        string `json:"cron,omitempty"`
	Every       string `json:"every,omitempty"`
	At          string `json:"at,omitempty"`
	Message     string `json:"message"`
	SessionMode string `json:"session_mode"`
	Enabled     bool   `json:"enabled"`
	AgentID     string `json:"agent_id,omitempty"`
	UserID      int64  `json:"user_id,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

func (s *Server) listSchedulerJobs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListSchedulerJobs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jobs := make([]schedulerJobJSON, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, dbRowToJobJSON(row))
	}
	writeData(w, http.StatusOK, jobs)
}

func (s *Server) createSchedulerJob(w http.ResponseWriter, r *http.Request) {
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

	id := generateShortID()
	enabled := int64(0)
	if body.Enabled {
		enabled = 1
	}

	_, err := s.q.CreateSchedulerJob(r.Context(), sqlc.CreateSchedulerJobParams{
		ID:            id,
		Name:          body.Name,
		ScheduleCron:  body.Cron,
		ScheduleEvery: body.Every,
		ScheduleAt:    body.At,
		Message:       body.Message,
		SessionMode:   body.SessionMode,
		Enabled:       enabled,
		AgentID:       sql.NullString{String: body.AgentID, Valid: body.AgentID != ""},
		UserID:        sql.NullInt64{Int64: body.UserID, Valid: body.UserID != 0},
		CreatedAt:     time.Now().UTC().Format("2006-01-02 15:04:05"),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	body.ID = id
	writeData(w, http.StatusCreated, body)
}

func (s *Server) updateSchedulerJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Verify job exists.
	existing, err := s.q.GetSchedulerJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
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

	enabled := int64(0)
	if body.Enabled {
		enabled = 1
	}

	err = s.q.UpdateSchedulerJob(r.Context(), sqlc.UpdateSchedulerJobParams{
		ID:            id,
		Name:          body.Name,
		ScheduleCron:  body.Cron,
		ScheduleEvery: body.Every,
		ScheduleAt:    body.At,
		Message:       body.Message,
		SessionMode:   body.SessionMode,
		Enabled:       enabled,
		AgentID:       sql.NullString{String: body.AgentID, Valid: body.AgentID != ""},
		UserID:        sql.NullInt64{Int64: body.UserID, Valid: body.UserID != 0},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	body.ID = id
	writeData(w, http.StatusOK, body)
}

func (s *Server) deleteSchedulerJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Verify job exists.
	if _, err := s.q.GetSchedulerJob(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if err := s.q.DeleteSchedulerJob(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func dbRowToJobJSON(row sqlc.SchedJob) schedulerJobJSON {
	j := schedulerJobJSON{
		ID:          row.ID,
		Name:        row.Name,
		Cron:        row.ScheduleCron,
		Every:       row.ScheduleEvery,
		At:          row.ScheduleAt,
		Message:     row.Message,
		SessionMode: row.SessionMode,
		Enabled:     row.Enabled != 0,
		CreatedAt:   row.CreatedAt,
	}
	if row.AgentID.Valid {
		j.AgentID = row.AgentID.String
	}
	if row.UserID.Valid {
		j.UserID = row.UserID.Int64
	}
	return j
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
