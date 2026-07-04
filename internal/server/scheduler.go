package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// isSystemOrPlugin reports whether a job's owner kind is a platform-level
// owner (system or plugin). These jobs are invisible to all API callers.
func isSystemOrPlugin(ownerKind string) bool {
	return ownerKind == scheduler.JobOwnerPlugin || ownerKind == scheduler.JobOwnerSystem
}

// ListJobTemplates returns all registered job templates with subscription
// status resolved for the current user.
// Returns 503 when the scheduler service is not available.
func (s *Server) ListJobTemplates(w http.ResponseWriter, r *http.Request) {
	if s.schedulerSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	templates := s.schedulerSvc.Templates()

	// Build a lookup: template key → (subscribed job ID, agent ID) for this user.
	type subInfo struct{ jobID, agentID string }
	subscriptions := make(map[string]subInfo)
	for _, job := range s.schedulerSvc.ListJobs() {
		if job.OwnerKind == scheduler.JobOwnerUser && job.UserID == info.UserID && job.JobKey != "" {
			subscriptions[job.JobKey] = subInfo{jobID: job.ID, agentID: job.AgentID}
		}
	}

	out := make([]apitypes.JobTemplate, 0, len(templates))
	for _, tmpl := range templates {
		t := apitypes.JobTemplate{
			Key:             tmpl.Key,
			Name:            tmpl.Name,
			Description:     tmpl.Description,
			DefaultSchedule: templateDefaultSchedule(tmpl.DefaultSchedule),
			SessionMode:     tmpl.SessionMode,
		}
		if sub, ok := subscriptions[tmpl.Key]; ok {
			t.SubscribedJobId = ptrStr(sub.jobID)
			if sub.agentID != "" {
				t.SubscribedAgentId = ptrStr(sub.agentID)
			}
		}
		out = append(out, t)
	}
	writeData(w, http.StatusOK, apitypes.JobTemplateList{JobTemplates: out})
}

func (s *Server) ListSchedulerJobs(w http.ResponseWriter, r *http.Request, agentID string) {
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	info := UserFromContext(r.Context())

	rows, err := s.q.ListSchedulerJobsByAgent(r.Context(), sqlc.ListSchedulerJobsByAgentParams{
		AgentID: pgtype.Text{String: agentID, Valid: agentID != ""},
		UserID:  pgtype.Text{String: info.UserID, Valid: info.UserID != ""},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list scheduler jobs")
		return
	}

	jobs := make([]apiserver.Job, 0, len(rows))
	for _, row := range rows {
		// Platform jobs (system/plugin) are invisible to all callers, including admins.
		if isSystemOrPlugin(row.OwnerKind) {
			continue
		}
		jobs = append(jobs, s.dbRowToAPIJob(row))
	}
	writeData(w, http.StatusOK, apiserver.JobList{Jobs: jobs})
}

func (s *Server) CreateSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string) {
	info := UserFromContext(r.Context())
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	if info == nil {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	if s.schedulerSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}

	var body apiserver.JobInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	userID := info.UserID

	// Template-subscription path.
	if body.TemplateKey != nil && *body.TemplateKey != "" {
		if body.Message != nil && *body.Message != "" {
			writeError(w, http.StatusBadRequest, "message must be empty when subscribing via template_key")
			return
		}
		sched := scheduler.Schedule{
			Cron:  derefStr(body.Cron),
			Every: derefStr(body.Every),
			At:    derefStr(body.At),
		}
		job, err := s.schedulerSvc.Subscribe(r.Context(), userID, agentID, *body.TemplateKey, sched)
		if err != nil {
			switch {
			case errors.Is(err, scheduler.ErrAlreadySubscribed):
				writeError(w, http.StatusConflict, "already subscribed to this template")
			case errors.Is(err, scheduler.ErrTemplateNotFound):
				writeError(w, http.StatusNotFound, "template not found")
			default:
				s.writeInternalError(w, err)
			}
			return
		}
		writeData(w, http.StatusCreated, s.schedulerJobToAPI(job))
		return
	}

	// Regular (non-template) job path.
	dispatchKind := scheduler.DispatchKindChat
	if body.DispatchKind != nil && *body.DispatchKind != "" {
		dispatchKind = string(*body.DispatchKind)
	}
	if dispatchKind != scheduler.DispatchKindChat && dispatchKind != scheduler.DispatchKindWorkflow {
		writeError(w, http.StatusBadRequest, "invalid dispatch_kind")
		return
	}
	if body.Name == nil || *body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if dispatchKind == scheduler.DispatchKindChat && (body.Message == nil || *body.Message == "") {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	if dispatchKind == scheduler.DispatchKindWorkflow && body.Message != nil && *body.Message != "" {
		writeError(w, http.StatusBadRequest, "message must be empty for workflow jobs")
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

	sched := scheduler.Schedule{
		Cron:  derefStr(body.Cron),
		Every: derefStr(body.Every),
		At:    derefStr(body.At),
	}
	var job scheduler.Job
	var err error
	if dispatchKind == scheduler.DispatchKindWorkflow {
		if body.WorkflowId == nil || *body.WorkflowId == "" {
			writeError(w, http.StatusBadRequest, "workflow_id is required for workflow jobs")
			return
		}
		job, err = s.schedulerSvc.AddWorkflowJobWithOwner(r.Context(), *body.Name, sched, sessionMode, agentID, userID, *body.WorkflowId, derefStringMap(body.Inputs), body.AllowReplan != nil && *body.AllowReplan)
	} else {
		job, err = s.schedulerSvc.AddJobWithOwner(*body.Name, derefStr(body.Message), sched, sessionMode, agentID, userID)
	}
	if err != nil {
		switch {
		case errors.Is(err, scheduler.ErrWorkflowJobNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, scheduler.ErrWorkflowJobValidation):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			s.writeInternalError(w, err)
		}
		return
	}
	writeData(w, http.StatusCreated, s.schedulerJobToAPI(job))
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

	// Platform jobs return 404 uniformly to avoid existence probing.
	if isSystemOrPlugin(existing.OwnerKind) {
		writeError(w, http.StatusNotFound, "job not found")
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

	writeData(w, http.StatusOK, s.dbRowToAPIJob(existing))
}

func (s *Server) UpdateSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string, jobID string) {
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	info := UserFromContext(r.Context())
	if s.schedulerSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}

	existing, err := s.q.GetSchedulerJob(r.Context(), jobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	// Platform jobs return 404 uniformly.
	if isSystemOrPlugin(existing.OwnerKind) {
		writeError(w, http.StatusNotFound, "job not found")
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

	update := scheduler.JobUpdate{}
	if body.Name != nil && *body.Name != "" {
		update.Name = body.Name
	}
	if body.Message != nil {
		update.Message = body.Message
	}
	if body.DispatchKind != nil && *body.DispatchKind != "" {
		dispatchKind := string(*body.DispatchKind)
		update.DispatchKind = &dispatchKind
	}
	if body.WorkflowId != nil || body.Inputs != nil || body.AllowReplan != nil {
		payload := decodeSchedulerPayload(existing.Payload)
		if body.WorkflowId != nil {
			payload["workflow_id"] = *body.WorkflowId
		}
		if body.Inputs != nil {
			payload["inputs"] = derefStringMap(body.Inputs)
		}
		if body.AllowReplan != nil {
			payload["allow_replan"] = *body.AllowReplan
		}
		update.Payload = payload
	}
	if body.Cron != nil || body.Every != nil || body.At != nil {
		sched := scheduler.Schedule{
			Cron:  derefStr(body.Cron),
			Every: derefStr(body.Every),
			At:    derefStr(body.At),
		}
		update.Schedule = &sched
	}
	if body.SessionMode != nil && *body.SessionMode != "" {
		update.SessionMode = body.SessionMode
	}
	if body.Enabled != nil {
		update.Enabled = body.Enabled
	}

	job, err := s.schedulerSvc.UpdateUserJob(r.Context(), jobID, update)
	if err != nil {
		switch {
		case errors.Is(err, scheduler.ErrSubscriptionMessageReadOnly):
			writeError(w, http.StatusBadRequest, "subscription job message is read-only; it is controlled by the template")
		case errors.Is(err, scheduler.ErrWorkflowJobNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, scheduler.ErrWorkflowJobValidation):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, scheduler.ErrOneTimeJobPast):
			// Fired one-time jobs are retired as disabled rows; re-enabling one
			// is a user mistake, not a server fault. They must set a new time.
			writeError(w, http.StatusBadRequest, "one-time job timestamp is in the past; set a new time to re-enable it")
		default:
			s.writeInternalError(w, err)
		}
		return
	}
	writeData(w, http.StatusOK, s.schedulerJobToAPI(job))
}

func (s *Server) DeleteSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string, jobID string) {
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	info := UserFromContext(r.Context())
	if s.schedulerSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}

	existing, err := s.q.GetSchedulerJob(r.Context(), jobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	// Platform jobs return 404 uniformly.
	if isSystemOrPlugin(existing.OwnerKind) {
		writeError(w, http.StatusNotFound, "job not found")
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

	if err := s.schedulerSvc.RemoveJob(jobID); err != nil {
		s.writeInternalError(w, err)
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

	// Platform jobs return 404 uniformly.
	if isSystemOrPlugin(existing.OwnerKind) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusForbidden, "access denied")
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

	// Platform jobs return 404 uniformly.
	if isSystemOrPlugin(existing.OwnerKind) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	info := UserFromContext(r.Context())
	if !existing.AgentID.Valid || existing.AgentID.String != agentID {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if info == nil || !existing.UserID.Valid || existing.UserID.String != info.UserID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	rows, err := s.q.ListSchedJobRuns(r.Context(), sqlc.ListSchedJobRunsParams{
		JobID:  jobID,
		UserID: pgtype.Text{},
		Limit:  int32(limit + 1),
		Offset: int32(offset),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list job runs")
		return
	}

	page, nextToken := nextPageTokenForRows(rows, limit, offset)
	runs := make([]apitypes.JobRun, 0, len(page))
	for _, row := range page {
		j := dbRowToAPIJobRun(row)
		if j.SessionId != "" {
			agent, err := s.q.GetConversationAgentBySessionID(r.Context(), sqlc.GetConversationAgentBySessionIDParams{
				SessionID: j.SessionId,
				UserID:    row.UserID,
			})
			switch {
			case err != nil:
				j.SessionId = ""
			case agent.Valid:
				j.SessionAgentId = ptrStr(agent.String)
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

// dbRowToAPIJob converts a DB row to an API Job, resolving the template message
// for subscription instances so the caller sees the actual prompt.
func (s *Server) dbRowToAPIJob(row sqlc.SchedJob) apiserver.Job {
	j := apiserver.Job{
		Id:           row.ID,
		OwnerKind:    ptrStr(row.OwnerKind),
		PluginId:     ptrStr(row.PluginID),
		JobKey:       ptrStr(row.JobKey),
		RuntimeName:  ptrStr(row.RuntimeName),
		Name:         row.Name,
		Description:  ptrStr(row.Description),
		Cron:         ptrStr(row.ScheduleCron),
		Every:        ptrStr(row.ScheduleEvery),
		At:           ptrStr(row.ScheduleAt),
		DispatchKind: apiJobDispatchKind(row.DispatchKind),
		SessionMode:  row.SessionMode,
		Enabled:      row.Enabled,
		CreatedAt:    ptrTime(row.CreatedAt.UTC()),
		UpdatedAt:    ptrTime(row.UpdatedAt.UTC()),
		LastError:    ptrStr(row.LastError),
	}
	// For subscription instances: return the template-resolved message and
	// surface template_key so the UI can display the badge and lock message.
	if row.JobKey != "" && s.schedulerSvc != nil {
		j.TemplateKey = ptrStr(row.JobKey)
		if msg, ok := s.schedulerSvc.ResolveTemplateMessage(row.JobKey); ok {
			j.Message = msg
		} else {
			j.Message = row.Message
		}
	} else {
		j.Message = row.Message
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

func (s *Server) schedulerJobToAPI(job scheduler.Job) apiserver.Job {
	j := apiserver.Job{
		Id:           job.ID,
		OwnerKind:    ptrStr(job.OwnerKind),
		PluginId:     ptrStr(job.PluginID),
		JobKey:       ptrStr(job.JobKey),
		RuntimeName:  ptrStr(job.RuntimeName),
		Name:         job.Name,
		Description:  ptrStr(job.Description),
		Cron:         ptrStr(job.Schedule.Cron),
		Every:        ptrStr(job.Schedule.Every),
		At:           ptrStr(job.Schedule.At),
		DispatchKind: apiJobDispatchKind(job.DispatchKind),
		SessionMode:  job.SessionMode,
		Enabled:      job.Enabled,
		AgentId:      ptrStr(job.AgentID),
		UserId:       ptrStr(job.UserID),
		CreatedAt:    ptrTime(job.CreatedAt),
		UpdatedAt:    ptrTime(job.UpdatedAt),
		LastRunAt:    job.LastRunAt,
		LastError:    ptrStr(job.LastError),
	}
	// Subscription instances: surface template key and resolved message.
	if job.JobKey != "" {
		j.TemplateKey = ptrStr(job.JobKey)
		if s.schedulerSvc != nil {
			if msg, ok := s.schedulerSvc.ResolveTemplateMessage(job.JobKey); ok {
				j.Message = msg
			}
		}
	} else {
		j.Message = job.Message
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
		StartedAt: row.StartedAt.UTC(),
	}
	if row.Error != "" {
		j.Error = &row.Error
	}
	if row.Output != "" {
		j.Output = &row.Output
	}
	if row.UserID.Valid {
		j.UserId = ptrStr(row.UserID.String)
	}
	if row.RootGoalID.Valid {
		j.RootGoalId = ptrStr(row.RootGoalID.String)
	}
	if finished := parseTimePtr(row.FinishedAt); finished != nil {
		j.FinishedAt = finished
		if started := row.StartedAt.UTC(); !started.IsZero() {
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

func apiJobDispatchKind(kind string) *apitypes.JobDispatchKind {
	if kind == "" {
		kind = scheduler.DispatchKindChat
	}
	v := apitypes.JobDispatchKind(kind)
	return &v
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefStringMap(p *map[string]string) map[string]string {
	if p == nil || len(*p) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(*p))
	maps.Copy(out, *p)
	return out
}

func decodeSchedulerPayload(raw json.RawMessage) map[string]any {
	if len(raw) == 0 || string(raw) == "{}" {
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

// templateDefaultSchedule returns a human-readable schedule string for the
// template's default schedule. Cron is preferred, then Every, then At.
func templateDefaultSchedule(s scheduler.Schedule) string {
	if s.Cron != "" {
		return s.Cron
	}
	if s.Every != "" {
		return s.Every
	}
	return s.At
}
