package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/scheduler"
)

func schedulerServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authz.ErrNotFound):
		writeError(w, http.StatusNotFound, "job not found")
	case errors.Is(err, authz.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, "authentication required")
	case errors.Is(err, authz.ErrForbidden):
		writeError(w, http.StatusForbidden, "access denied")
	case errors.Is(err, scheduler.ErrWorkflowJobNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, scheduler.ErrWorkflowJobValidation):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, scheduler.ErrOneTimeJobPast):
		// Fired one-time jobs are retired as disabled rows; re-enabling one
		// is a user mistake, not a server fault. They must set a new time.
		writeError(w, http.StatusBadRequest, "one-time job timestamp is in the past; set a new time to re-enable it")
	case errors.Is(err, scheduler.ErrSubscriptionMessageReadOnly):
		writeError(w, http.StatusBadRequest, "subscription job message is read-only; it is controlled by the template")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// ListJobTemplates returns all registered job templates with subscription
// status resolved for the current user.
// Returns 503 when the scheduler service is not available.
func (s *Server) ListJobTemplates(w http.ResponseWriter, r *http.Request) {
	// The static template catalog is public to any authenticated caller, but the
	// per-user subscription metadata (which job/agent a template is bound to) is a
	// durable resource: resolve it through Scheduler's List + per-row Read rules,
	// never a raw ListJobs bypass.
	acc, ok := s.schedulerAccess(w, r)
	if !ok {
		return
	}

	templates := s.schedulerSvc.Templates()

	subs, err := acc.SubscribedTemplates(r.Context())
	if err != nil {
		schedulerServiceError(w, err)
		return
	}
	// Build a lookup: template key → (subscribed job ID, agent ID) for this user.
	type subInfo struct{ jobID, agentID string }
	subscriptions := make(map[string]subInfo)
	for _, job := range subs {
		subscriptions[job.JobKey] = subInfo{jobID: job.ID, agentID: job.AgentID}
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

// schedulerAccess captures Scheduler's direct authority for an authenticated
// caller. The Authority carries the verified session role, and Scheduler asks
// Agent to validate durable job targets so request path/body fields never
// contribute to authorization.
func (s *Server) schedulerAccess(w http.ResponseWriter, r *http.Request) (*scheduler.Access, bool) {
	if s.schedulerSvc == nil {
		writeCapabilityUnavailable(w, capScheduler)
		return nil, false
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	acc, err := s.schedulerSvc.Begin(r.Context(), authority)
	if err != nil {
		schedulerServiceError(w, err)
		return nil, false
	}
	return acc, true
}

func (s *Server) ListSchedulerJobs(w http.ResponseWriter, r *http.Request, agentID string) {
	acc, ok := s.schedulerAccess(w, r)
	if !ok {
		return
	}
	rows, err := acc.ListJobs(r.Context(), agentID)
	if err != nil {
		schedulerServiceError(w, err)
		return
	}

	jobs := make([]apiserver.Job, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, s.schedulerJobToAPI(row))
	}
	writeData(w, http.StatusOK, apiserver.JobList{Jobs: jobs})
}

func (s *Server) CreateSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string) {
	acc, ok := s.schedulerAccess(w, r)
	if !ok {
		return
	}

	var body apiserver.JobInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

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
		job, err := acc.Subscribe(r.Context(), agentID, *body.TemplateKey, sched)
		if err != nil {
			switch {
			case errors.Is(err, scheduler.ErrAlreadySubscribed):
				writeError(w, http.StatusConflict, "already subscribed to this template")
			case errors.Is(err, scheduler.ErrTemplateNotFound):
				writeError(w, http.StatusNotFound, "template not found")
			case errors.Is(err, authz.ErrForbidden), errors.Is(err, authz.ErrUnauthenticated):
				schedulerServiceError(w, err)
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
		job, err = acc.CreateWorkflowJob(r.Context(), *body.Name, sched, sessionMode, agentID, *body.WorkflowId, derefStringMap(body.Inputs), body.AllowReplan != nil && *body.AllowReplan)
	} else {
		job, err = acc.CreateJob(r.Context(), *body.Name, derefStr(body.Message), sched, sessionMode, agentID, derefStr(body.IdempotencyKey))
	}
	if err != nil {
		schedulerServiceError(w, err)
		return
	}
	writeData(w, http.StatusCreated, s.schedulerJobToAPI(job))
}

func (s *Server) GetSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string, jobID string) {
	acc, ok := s.schedulerAccess(w, r)
	if !ok {
		return
	}
	job, err := acc.GetJob(r.Context(), agentID, jobID)
	if err != nil {
		schedulerServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, s.schedulerJobToAPI(job))
}

func (s *Server) UpdateSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string, jobID string) {
	acc, ok := s.schedulerAccess(w, r)
	if !ok {
		return
	}
	existing, err := acc.GetJob(r.Context(), agentID, jobID)
	if err != nil {
		schedulerServiceError(w, err)
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
		payload := maps.Clone(existing.Payload)
		if payload == nil {
			payload = map[string]any{}
		}
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

	job, err := acc.UpdateJob(r.Context(), agentID, jobID, update)
	if err != nil {
		schedulerServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, s.schedulerJobToAPI(job))
}

func (s *Server) DeleteSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string, jobID string) {
	acc, ok := s.schedulerAccess(w, r)
	if !ok {
		return
	}
	if err := acc.DeleteJob(r.Context(), agentID, jobID); err != nil {
		schedulerServiceError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) TriggerSchedulerJob(w http.ResponseWriter, r *http.Request, agentID string, jobID string) {
	acc, ok := s.schedulerAccess(w, r)
	if !ok {
		return
	}
	runID, err := acc.RunJobNow(r.Context(), agentID, jobID)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) || errors.Is(err, authz.ErrForbidden) || errors.Is(err, authz.ErrUnauthenticated) {
			schedulerServiceError(w, err)
			return
		}
		writeError(w, http.StatusConflict, "resource conflict")
		return
	}

	resp := apitypes.TriggerJobResult{RunId: runID}
	writeData(w, http.StatusAccepted, resp)
}

func (s *Server) ListSchedulerJobRuns(w http.ResponseWriter, r *http.Request, agentID string, jobID string, params apiserver.ListSchedulerJobRunsParams) {
	acc, ok := s.schedulerAccess(w, r)
	if !ok {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	// The scheduler service owns the run read model (including the session's
	// executing agent), so the transport never queries scheduler or conversation
	// tables directly. It authorizes the job read through Scheduler's direct rule.
	rows, err := acc.ListRuns(r.Context(), agentID, jobID, limit+1, offset)
	if err != nil {
		schedulerServiceError(w, err)
		return
	}
	page, nextToken := nextPageTokenForRows(rows, limit, offset)
	runs := make([]apitypes.JobRun, 0, len(page))
	for _, row := range page {
		runs = append(runs, schedulerRunToAPI(row))
	}
	out := apitypes.JobRunList{Runs: runs}
	if nextToken != "" {
		out.NextPageToken = &nextToken
	}
	writeData(w, http.StatusOK, out)
}

// --------------- converter functions ---------------

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

func schedulerRunToAPI(row scheduler.JobRun) apitypes.JobRun {
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
	if row.UserID != "" {
		j.UserId = ptrStr(row.UserID)
	}
	if row.RootGoalID != "" {
		j.RootGoalId = ptrStr(row.RootGoalID)
	}
	if row.SessionAgentID != "" {
		j.SessionAgentId = ptrStr(row.SessionAgentID)
	}
	if row.FinishedAt != nil {
		finished := row.FinishedAt.UTC()
		j.FinishedAt = &finished
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
