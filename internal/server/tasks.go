package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/tasks"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// SetTasksService wires the v2 task system into the admin server.
func (s *Server) SetTasksService(svc *tasks.Service) { s.tasksSvc = svc }

const orgHeader = "X-Stella-Org-ID"

// resolveOrgID returns the org id for this request. D14 / MP4: the header
// X-Stella-Org-ID takes precedence; absent it the handler returns 400.
func (s *Server) resolveOrgID(r *http.Request) string {
	return r.Header.Get(orgHeader)
}

func (s *Server) ListTasks(w http.ResponseWriter, r *http.Request, params apiserver.ListTasksParams) {
	if s.tasksSvc == nil {
		tasksUnavailable(w)
		return
	}
	orgID := s.resolveOrgID(r)
	if orgID == "" {
		writeError(w, http.StatusBadRequest, "missing X-Stella-Org-ID header")
		return
	}
	limit := int64(50)
	offset := int64(0)
	if params.Limit != nil {
		limit = int64(*params.Limit)
	}
	if params.Offset != nil {
		offset = int64(*params.Offset)
	}
	list, err := s.tasksSvc.Facade.ListTasksByOrg(r.Context(), orgID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]apitypes.AgentTask, 0, len(list))
	for _, t := range list {
		items = append(items, toAPITask(t))
	}
	writeData(w, http.StatusOK, apitypes.AgentTaskList{Items: items})
}

func (s *Server) CreateTask(w http.ResponseWriter, r *http.Request) {
	if s.tasksSvc == nil {
		tasksUnavailable(w)
		return
	}
	orgID := s.resolveOrgID(r)
	if orgID == "" {
		writeError(w, http.StatusBadRequest, "missing X-Stella-Org-ID header")
		return
	}
	info := UserFromContext(r.Context())
	var userID string
	if info != nil {
		userID = info.UserID
	}
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "no authenticated user")
		return
	}

	var body apitypes.AgentTaskInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	in := tasks.CreateTaskInput{
		OrgID:  orgID,
		UserID: userID,
		Title:  body.Title,
	}
	if body.Description != nil {
		in.Description = *body.Description
	}
	if body.Priority != nil {
		in.Priority = string(*body.Priority)
	}
	if body.ExecutorAgentId != nil {
		in.ExecutorAgentID = *body.ExecutorAgentId
	}
	if body.MaxRetries != nil {
		in.MaxRetries = int64(*body.MaxRetries)
	}
	if body.NotBefore != nil {
		in.NotBefore = *body.NotBefore
	}
	if body.DeadlineAt != nil {
		in.DeadlineAt = *body.DeadlineAt
	}
	if body.Context != nil {
		b, _ := json.Marshal(*body.Context)
		in.Context = string(b)
	}
	if body.Activate != nil {
		in.ActivateOnCreate = *body.Activate
	}
	if body.Deps != nil {
		for _, d := range *body.Deps {
			dep := tasks.DepInput{DepTaskID: d.DepTaskId}
			if d.Kind != nil {
				dep.Kind = string(*d.Kind)
			}
			if d.OnFailure != nil {
				dep.OnFailure = string(*d.OnFailure)
			}
			in.Deps = append(in.Deps, dep)
		}
	}

	task, err := s.tasksSvc.Facade.CreateTask(r.Context(), in)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	writeData(w, http.StatusCreated, toAPITask(task))
}

func (s *Server) GetTask(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.tasksSvc == nil {
		tasksUnavailable(w)
		return
	}
	orgID := s.resolveOrgID(r)
	if orgID == "" {
		writeError(w, http.StatusBadRequest, "missing X-Stella-Org-ID header")
		return
	}
	task, err := s.tasksSvc.Facade.GetTask(r.Context(), taskID, orgID)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	writeData(w, http.StatusOK, toAPITask(task))
}

func (s *Server) CancelTask(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.tasksSvc == nil {
		tasksUnavailable(w)
		return
	}
	orgID := s.resolveOrgID(r)
	if orgID == "" {
		writeError(w, http.StatusBadRequest, "missing X-Stella-Org-ID header")
		return
	}
	info := UserFromContext(r.Context())
	actor := tasks.Actor{Type: tasks.ActorUser}
	if info != nil {
		actor.ID = info.UserID
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = decodeJSON(r, &body)
	if err := s.tasksSvc.Facade.CancelTask(r.Context(), taskID, body.Reason, actor); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	task, err := s.tasksSvc.Facade.GetTask(r.Context(), taskID, orgID)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	writeData(w, http.StatusOK, toAPITask(task))
}

func (s *Server) GetTaskReadiness(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.tasksSvc == nil {
		tasksUnavailable(w)
		return
	}
	rd, err := s.tasksSvc.Facade.GetReadiness(r.Context(), taskID)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	out := apitypes.AgentTaskReadiness{
		State:        apitypes.AgentTaskReadinessState(rd.State),
		Dispatchable: rd.Dispatchable,
	}
	if len(rd.Reasons) > 0 {
		reasons := make([]apitypes.AgentTaskReadinessReason, 0, len(rd.Reasons))
		for _, rr := range rd.Reasons {
			reasons = append(reasons, apitypes.AgentTaskReadinessReason{
				Type:       rr.Type,
				UpstreamId: optString(rr.UpstreamID),
				Detail:     optString(rr.Detail),
			})
		}
		out.Reasons = &reasons
	}
	writeData(w, http.StatusOK, out)
}

func tasksUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "task system v2 not yet initialized")
}

// toAPITask maps a sqlc.AgentTask to the API type.
func toAPITask(t sqlc.AgentTask) apitypes.AgentTask {
	out := apitypes.AgentTask{
		Id:        t.ID,
		OrgId:     t.OrgID,
		UserId:    t.UserID,
		Title:     t.Title,
		Status:    apitypes.AgentTaskStatus(t.Status),
		Priority:  apitypes.AgentTaskPriority(t.Priority),
		CreatedAt: parseTaskTime(t.CreatedAt),
		UpdatedAt: parseTaskTime(t.UpdatedAt),
	}
	if t.AgentID.Valid {
		out.AgentId = &t.AgentID.String
	}
	if t.Description != "" {
		out.Description = &t.Description
	}
	if t.Required != 0 {
		v := t.Required != 0
		out.RequiredFlag = &v
	}
	if t.RetryCount != 0 {
		v := int(t.RetryCount)
		out.RetryCount = &v
	}
	if t.MaxRetries != 0 {
		v := int(t.MaxRetries)
		out.MaxRetries = &v
	}
	if t.NotBefore.Valid && t.NotBefore.String != "" {
		nt := parseTaskTime(t.NotBefore.String)
		out.NotBefore = &nt
	}
	if t.DeadlineAt.Valid && t.DeadlineAt.String != "" {
		dt := parseTaskTime(t.DeadlineAt.String)
		out.DeadlineAt = &dt
	}
	if t.SessionID.Valid {
		out.SessionId = &t.SessionID.String
	}
	if t.ActiveRunID.Valid {
		out.ActiveRunId = &t.ActiveRunID.String
	}
	if t.ActiveBlockerID.Valid {
		out.ActiveBlockerId = &t.ActiveBlockerID.String
	}
	if t.Context != "" && t.Context != "{}" {
		var m map[string]any
		if err := json.Unmarshal([]byte(t.Context), &m); err == nil {
			out.Context = &m
		}
	}
	if t.Output != "" && t.Output != "{}" {
		var m map[string]any
		if err := json.Unmarshal([]byte(t.Output), &m); err == nil {
			out.Output = &m
		}
	}
	if t.CompletedAt.Valid && t.CompletedAt.String != "" {
		ct := parseTaskTime(t.CompletedAt.String)
		out.CompletedAt = &ct
	}
	if t.CancelledAt.Valid && t.CancelledAt.String != "" {
		ct := parseTaskTime(t.CancelledAt.String)
		out.CancelledAt = &ct
	}
	return out
}

func parseTaskTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func statusForError(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case strings.Contains(err.Error(), "not found"):
		return http.StatusNotFound
	case strings.Contains(err.Error(), "invalid status transition"):
		return http.StatusConflict
	case strings.Contains(err.Error(), "missing"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
