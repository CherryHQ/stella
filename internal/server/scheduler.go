package server

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const adminDBTimeLayout = "2006-01-02 15:04:05"

func (s *Server) ListSchedulerJobs(w http.ResponseWriter, r *http.Request, agentID string) {
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	info := UserFromContext(r.Context())

	rows, err := s.q.ListSchedulerJobsByAgent(r.Context(), sqlc.ListSchedulerJobsByAgentParams{
		AgentID: sql.NullString{String: agentID, Valid: agentID != ""},
		UserID:  sql.NullString{String: info.UserID, Valid: info.UserID != ""},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list scheduler jobs")
		return
	}

	jobs := make([]apiserver.Job, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, dbRowToAPIJob(row))
	}
	writeData(w, http.StatusOK, apiserver.JobList{Jobs: jobs})
}

func (s *Server) CreateSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string) {
	info := UserFromContext(r.Context())
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	var body apiserver.JobInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Name == nil || *body.Name == "" || body.Message == nil || *body.Message == "" {
		writeError(w, http.StatusBadRequest, "name and message are required")
		return
	}
	if err := validateScheduleInput(body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	sessionMode := "reuse"
	if body.SessionMode != nil && *body.SessionMode != "" {
		sessionMode = *body.SessionMode
	}

	if info == nil {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	userID := info.UserID

	if s.schedulerSvc != nil {
		sched := scheduler.Schedule{
			Cron:  derefStr(body.Cron),
			Every: derefStr(body.Every),
			At:    derefStr(body.At),
		}
		job, err := s.schedulerSvc.AddJobWithOwner(*body.Name, *body.Message, sched, sessionMode, agentID, userID)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		writeData(w, http.StatusCreated, schedulerJobToAPI(job))
		return
	}

	// Infer exec_scope from user_id when not explicitly set.
	execScope := scheduler.ExecScopeSystem
	if userID != "" {
		execScope = scheduler.ExecScopeUser
	}

	id := generateShortID()
	var enabled int64
	if body.Enabled != nil && *body.Enabled {
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
		Name:          *body.Name,
		Description:   derefStr(body.Description),
		ScheduleCron:  derefStr(body.Cron),
		ScheduleEvery: derefStr(body.Every),
		ScheduleAt:    derefStr(body.At),
		Message:       *body.Message,
		Payload:       "{}",
		SessionMode:   sessionMode,
		Enabled:       enabled,
		AgentID:       sql.NullString{String: agentID, Valid: agentID != ""},
		UserID:        sql.NullString{String: userID, Valid: userID != ""},
		CreatedAt:     now,
		UpdatedAt:     now,
		LastRunAt:     sql.NullString{},
		LastError:     "",
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	nowT := ptrTime(parseTime(now))
	resp := apiserver.Job{
		Id:          id,
		OwnerKind:   ptrStr(scheduler.JobOwnerUser),
		Name:        *body.Name,
		Description: body.Description,
		Cron:        body.Cron,
		Every:       body.Every,
		At:          body.At,
		Message:     *body.Message,
		SessionMode: sessionMode,
		Enabled:     enabled != 0,
		AgentId:     ptrStr(agentID),
		UserId:      ptrStr(userID),
		CreatedAt:   nowT,
		UpdatedAt:   nowT,
	}
	writeData(w, http.StatusCreated, resp)
}

func (s *Server) GetSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string, jobID string) {
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	info := UserFromContext(r.Context())

	existing, err := s.q.GetSchedulerJob(r.Context(), jobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	isGlobal := existing.OwnerKind == scheduler.JobOwnerPlugin || existing.OwnerKind == scheduler.JobOwnerSystem
	if !isGlobal && (!existing.AgentID.Valid || existing.AgentID.String != agentID) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if !isGlobal && (info == nil || !existing.UserID.Valid || existing.UserID.String != info.UserID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	writeData(w, http.StatusOK, dbRowToAPIJob(existing))
}

func (s *Server) UpdateSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string, jobID string) {
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	info := UserFromContext(r.Context())

	existing, err := s.q.GetSchedulerJob(r.Context(), jobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if existing.OwnerKind == scheduler.JobOwnerPlugin {
		writeError(w, http.StatusForbidden, "plugin-owned jobs are read-only in admin")
		return
	}
	if !existing.AgentID.Valid || existing.AgentID.String != agentID {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if info == nil || !existing.UserID.Valid || existing.UserID.String != info.UserID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	var body apiserver.JobInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Merge: use existing values for nil/empty fields.
	name := existing.Name
	if body.Name != nil && *body.Name != "" {
		name = *body.Name
	}
	message := existing.Message
	if body.Message != nil && *body.Message != "" {
		message = *body.Message
	}
	cron := existing.ScheduleCron
	every := existing.ScheduleEvery
	at := existing.ScheduleAt
	if body.Cron != nil || body.Every != nil || body.At != nil {
		cron = derefStr(body.Cron)
		every = derefStr(body.Every)
		at = derefStr(body.At)
	}
	sessionMode := existing.SessionMode
	if body.SessionMode != nil && *body.SessionMode != "" {
		sessionMode = *body.SessionMode
	}

	userID := info.UserID

	var enabled int64
	if body.Enabled != nil {
		if *body.Enabled {
			enabled = 1
		}
	} else {
		enabled = existing.Enabled
	}

	now := time.Now().UTC().Format(adminDBTimeLayout)
	err = s.q.UpdateSchedulerJob(r.Context(), sqlc.UpdateSchedulerJobParams{
		ID:            jobID,
		OwnerKind:     existing.OwnerKind,
		ExecScope:     existing.ExecScope,
		PluginID:      existing.PluginID,
		JobKey:        existing.JobKey,
		RuntimeName:   existing.RuntimeName,
		Name:          name,
		Description:   existing.Description,
		ScheduleCron:  cron,
		ScheduleEvery: every,
		ScheduleAt:    at,
		Message:       message,
		Payload:       existing.Payload,
		SessionMode:   sessionMode,
		Enabled:       enabled,
		AgentID:       sql.NullString{String: agentID, Valid: agentID != ""},
		UserID:        sql.NullString{String: userID, Valid: userID != ""},
		UpdatedAt:     now,
		LastRunAt:     existing.LastRunAt,
		LastError:     existing.LastError,
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	resp := apiserver.Job{
		Id:          jobID,
		OwnerKind:   ptrStr(existing.OwnerKind),
		PluginId:    ptrStr(existing.PluginID),
		JobKey:      ptrStr(existing.JobKey),
		RuntimeName: ptrStr(existing.RuntimeName),
		Name:        name,
		Description: ptrStr(existing.Description),
		Cron:        ptrStr(cron),
		Every:       ptrStr(every),
		At:          ptrStr(at),
		Message:     message,
		SessionMode: sessionMode,
		Enabled:     enabled != 0,
		AgentId:     ptrStr(agentID),
		UserId:      ptrStr(userID),
		CreatedAt:   ptrTime(parseTime(existing.CreatedAt)),
		UpdatedAt:   ptrTime(parseTime(now)),
		LastRunAt:   parseTimePtr(existing.LastRunAt),
		LastError:   ptrStr(existing.LastError),
	}
	writeData(w, http.StatusOK, resp)
}

func (s *Server) DeleteSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string, jobID string) {
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	info := UserFromContext(r.Context())

	existing, err := s.q.GetSchedulerJob(r.Context(), jobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if existing.OwnerKind == scheduler.JobOwnerPlugin {
		writeError(w, http.StatusForbidden, "plugin-owned jobs are read-only in admin")
		return
	}
	if !existing.AgentID.Valid || existing.AgentID.String != agentID {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if info == nil || !existing.UserID.Valid || existing.UserID.String != info.UserID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	var deleteErr error
	if s.schedulerSvc != nil {
		deleteErr = s.schedulerSvc.RemoveJob(jobID)
	} else {
		deleteErr = s.q.DeleteSchedulerJob(r.Context(), jobID)
	}
	if deleteErr != nil {
		writeError(w, http.StatusInternalServerError, deleteErr.Error())
		return
	}
	writeNoContent(w)
}

func (s *Server) TriggerSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string, jobID string) {
	if s.schedulerSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	existing, err := s.q.GetSchedulerJob(r.Context(), jobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	if existing.OwnerKind == scheduler.JobOwnerPlugin || existing.OwnerKind == scheduler.JobOwnerSystem {
		writeError(w, http.StatusForbidden, "cannot manually trigger system or plugin jobs")
		return
	}
	if !existing.AgentID.Valid || existing.AgentID.String != agentID {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if !existing.UserID.Valid || existing.UserID.String != info.UserID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	runID, err := s.schedulerSvc.RunJobNow(r.Context(), jobID)
	if err != nil {
		writeError(w, http.StatusConflict, "resource conflict")
		return
	}

	resp := apitypes.TriggerJobResult{RunId: runID}
	writeData(w, http.StatusAccepted, resp)
}

func (s *Server) ListSchedulerJobRuns(w http.ResponseWriter, r *http.Request, agentID string, jobID string, params apiserver.ListSchedulerJobRunsParams) {
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	existing, err := s.q.GetSchedulerJob(r.Context(), jobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	info := UserFromContext(r.Context())
	isGlobal := existing.OwnerKind == scheduler.JobOwnerPlugin || existing.OwnerKind == scheduler.JobOwnerSystem
	if !isGlobal && (!existing.AgentID.Valid || existing.AgentID.String != agentID) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if !isGlobal && (info == nil || !existing.UserID.Valid || existing.UserID.String != info.UserID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	// For global (plugin/system) jobs the caller only sees their own runs, so
	// scope by user in SQL; for owned jobs access is already verified above and
	// every run belongs to the owner. Filtering in the query keeps the limit+1
	// page-size detection and next_page_token accurate.
	var userFilter any
	if isGlobal && info != nil {
		userFilter = info.UserID
	}
	rows, err := s.q.ListSchedJobRuns(r.Context(), sqlc.ListSchedJobRunsParams{
		JobID:  jobID,
		UserID: userFilter,
		Limit:  int64(limit + 1),
		Offset: int64(offset),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list job runs")
		return
	}

	page, nextToken := nextPageTokenForRows(rows, limit, offset)
	sm, _ := s.mem.(memory.SessionManager)
	runs := make([]apitypes.JobRun, 0, len(page))
	for _, row := range page {
		j := dbRowToAPIJobRun(row)
		if j.SessionId != "" && sm != nil {
			ctx := r.Context()
			if row.UserID.Valid {
				ctx = memory.WithUserID(ctx, row.UserID.String)
			}
			if _, err := sm.LoadInfo(ctx, j.SessionId); err != nil {
				j.SessionId = ""
			}
		}
		runs = append(runs, j)
	}
	out := apitypes.JobRunList{Runs: runs}
	if nextToken != "" {
		out.NextPageToken = &nextToken
	}
	writeData(w, http.StatusOK, out)
}

// --------------- converter functions ---------------

func dbRowToAPIJob(row sqlc.SchedJob) apiserver.Job {
	j := apiserver.Job{
		Id:          row.ID,
		OwnerKind:   ptrStr(row.OwnerKind),
		PluginId:    ptrStr(row.PluginID),
		JobKey:      ptrStr(row.JobKey),
		RuntimeName: ptrStr(row.RuntimeName),
		Name:        row.Name,
		Description: ptrStr(row.Description),
		Cron:        ptrStr(row.ScheduleCron),
		Every:       ptrStr(row.ScheduleEvery),
		At:          ptrStr(row.ScheduleAt),
		Message:     row.Message,
		SessionMode: row.SessionMode,
		Enabled:     row.Enabled != 0,
		CreatedAt:   ptrTime(parseTime(row.CreatedAt)),
		UpdatedAt:   ptrTime(parseTime(row.UpdatedAt)),
		LastError:   ptrStr(row.LastError),
	}
	if payload := decodeSchedulerPayload(row.Payload); len(payload) > 0 {
		j.Payload = &payload
	}
	if row.AgentID.Valid {
		j.AgentId = ptrStr(row.AgentID.String)
	}
	if row.UserID.Valid {
		j.UserId = ptrStr(row.UserID.String)
	}
	j.LastRunAt = parseTimePtr(row.LastRunAt)
	return j
}

func schedulerJobToAPI(job scheduler.Job) apiserver.Job {
	j := apiserver.Job{
		Id:          job.ID,
		OwnerKind:   ptrStr(job.OwnerKind),
		PluginId:    ptrStr(job.PluginID),
		JobKey:      ptrStr(job.JobKey),
		RuntimeName: ptrStr(job.RuntimeName),
		Name:        job.Name,
		Description: ptrStr(job.Description),
		Cron:        ptrStr(job.Schedule.Cron),
		Every:       ptrStr(job.Schedule.Every),
		At:          ptrStr(job.Schedule.At),
		Message:     job.Message,
		SessionMode: job.SessionMode,
		Enabled:     job.Enabled,
		AgentId:     ptrStr(job.AgentID),
		UserId:      ptrStr(job.UserID),
		CreatedAt:   ptrTime(job.CreatedAt),
		UpdatedAt:   ptrTime(job.UpdatedAt),
		LastRunAt:   job.LastRunAt,
		LastError:   ptrStr(job.LastError),
	}
	if len(job.Payload) > 0 {
		j.Payload = &job.Payload
	}
	return j
}

func dbRowToAPIJobRun(row sqlc.SchedJobRun) apitypes.JobRun {
	j := apitypes.JobRun{
		Id:        row.ID,
		JobId:     row.JobID,
		SessionId: row.SessionID,
		Status:    row.Status,
		StartedAt: parseTime(row.StartedAt),
	}
	if row.Error != "" {
		j.Error = &row.Error
	}
	if row.UserID.Valid {
		j.UserId = ptrStr(row.UserID.String)
	}
	if finished := parseTimePtr(row.FinishedAt); finished != nil {
		j.FinishedAt = finished
		if started := parseTime(row.StartedAt); !started.IsZero() {
			dur := finished.Sub(started).Truncate(time.Second).String()
			j.Duration = &dur
		}
	}
	return j
}

// --------------- helpers ---------------

func ptrTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
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

func validateScheduleInput(body apiserver.JobInput) error {
	count := 0
	if body.Cron != nil && *body.Cron != "" {
		count++
	}
	if body.Every != nil && *body.Every != "" {
		count++
	}
	if body.At != nil && *body.At != "" {
		count++
	}
	if count == 0 {
		return fmt.Errorf("schedule requires one of: cron, every, or at")
	}
	if count > 1 {
		return fmt.Errorf("schedule must have exactly one of: cron, every, or at")
	}
	if body.Every != nil && *body.Every != "" {
		if _, err := time.ParseDuration(*body.Every); err != nil {
			return fmt.Errorf("invalid duration %q: %w", *body.Every, err)
		}
	}
	return nil
}

func generateShortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
