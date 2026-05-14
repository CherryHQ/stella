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

// SetTasksService wires the tasks service into the admin server.
func (s *Server) SetTasksService(svc *tasks.Service) {
	s.tasksSvc = svc
}

func (s *Server) ListAgentTasks(w http.ResponseWriter, r *http.Request, params apiserver.ListAgentTasksParams) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())
	var userID int64
	isAdmin := false
	if info != nil {
		userID = info.UserID
		isAdmin = info.IsAdmin
	}

	var statusFilter string
	if params.Status != nil {
		statusFilter = *params.Status
	}

	list, err := s.tasksSvc.ListTasks(r.Context(), userID, isAdmin, statusFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]apiserver.AgentTask, 0, len(list))
	for _, t := range list {
		items = append(items, toAPITask(t))
	}
	writeData(w, http.StatusOK, apiserver.AgentTaskList{Items: items})
}

func (s *Server) CreateAgentTask(w http.ResponseWriter, r *http.Request) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())

	var body apiserver.AgentTaskInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	var userID int64
	if info != nil {
		userID = info.UserID
	}

	agentID := ""
	if body.AgentId != nil {
		agentID = *body.AgentId
	}
	description := ""
	if body.Description != nil {
		description = *body.Description
	}
	priority := ""
	if body.Priority != nil {
		priority = string(*body.Priority)
	}

	var deps []string
	if body.Deps != nil {
		deps = *body.Deps
	}
	task, err := s.tasksSvc.CreateTask(r.Context(), tasks.CreateTaskParams{
		Title:       body.Title,
		Description: description,
		Priority:    priority,
		AgentID:     agentID,
		UserID:      userID,
		Deps:        deps,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, toAPITask(task))
}

func (s *Server) GetAgentTask(w http.ResponseWriter, r *http.Request, id string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())

	task, err := s.tasksSvc.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	if info != nil && !info.IsAdmin && task.UserID != info.UserID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	writeData(w, http.StatusOK, toAPITask(task))
}

func (s *Server) UpdateAgentTask(w http.ResponseWriter, r *http.Request, id string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())
	var userID int64
	isAdmin := false
	if info != nil {
		userID = info.UserID
		isAdmin = info.IsAdmin
	}

	var body apiserver.AgentTaskUpdate
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	title := ""
	if body.Title != nil {
		title = *body.Title
	}
	description := ""
	if body.Description != nil {
		description = *body.Description
	}
	priority := ""
	if body.Priority != nil {
		priority = string(*body.Priority)
	}
	agentID := ""
	if body.AgentId != nil {
		agentID = *body.AgentId
	}

	task, err := s.tasksSvc.UpdateTask(r.Context(), id, userID, isAdmin, tasks.UpdateTaskParams{
		Title:       title,
		Description: description,
		Priority:    priority,
		AgentID:     agentID,
	})
	if err != nil {
		if strings.Contains(err.Error(), "forbidden") {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, toAPITask(task))
}

func (s *Server) DeleteAgentTask(w http.ResponseWriter, r *http.Request, id string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())
	var userID int64
	isAdmin := false
	if info != nil {
		userID = info.UserID
		isAdmin = info.IsAdmin
	}

	if err := s.tasksSvc.DeleteTask(r.Context(), id, userID, isAdmin); err != nil {
		if strings.Contains(err.Error(), "forbidden") {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, apiserver.DeleteResult{Status: "deleted"})
}

func (s *Server) AgentTaskAction(w http.ResponseWriter, r *http.Request, id string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())
	var userID int64
	isAdmin := false
	if info != nil {
		userID = info.UserID
		isAdmin = info.IsAdmin
	}

	var body apiserver.AgentTaskAction
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	message := ""
	if body.Message != nil {
		message = *body.Message
	}

	task, err := s.tasksSvc.HandleAction(r.Context(), id, userID, isAdmin, tasks.ActionParams{
		Action:  string(body.Type),
		Message: message,
	})
	if err != nil {
		if strings.Contains(err.Error(), "forbidden") {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, toAPITask(task))
}

func (s *Server) ListAgentTaskEvents(w http.ResponseWriter, r *http.Request, id string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())

	task, err := s.tasksSvc.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if info != nil && !info.IsAdmin && task.UserID != info.UserID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	events, err := s.tasksSvc.ListTaskEvents(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]apiserver.AgentTaskEvent, 0, len(events))
	for _, e := range events {
		items = append(items, toAPITaskEvent(e))
	}
	writeData(w, http.StatusOK, apiserver.AgentTaskEventList{Items: items})
}

// toAPITask maps a sqlc.AgentTask to the API type.
func toAPITask(t sqlc.AgentTask) apiserver.AgentTask {
	at := apiserver.AgentTask{
		Id:        t.ID,
		Title:     t.Title,
		Status:    apitypes.AgentTaskStatus(t.Status),
		Priority:  apitypes.AgentTaskPriority(t.Priority),
		CreatedAt: parseTaskTime(t.CreatedAt),
		UpdatedAt: parseTaskTime(t.UpdatedAt),
	}
	if t.Description != "" {
		at.Description = &t.Description
	}
	if t.AgentID.Valid && t.AgentID.String != "" {
		at.AgentId = &t.AgentID.String
	}
	if t.SessionID.Valid && t.SessionID.String != "" {
		at.SessionId = &t.SessionID.String
	}
	if t.UserID != 0 {
		at.UserId = &t.UserID
	}
	if t.Context != "" && t.Context != "{}" {
		var ctx map[string]any
		if err := json.Unmarshal([]byte(t.Context), &ctx); err == nil && len(ctx) > 0 {
			at.Context = &ctx
		}
	}
	if t.ReviewRequest != "" && t.ReviewRequest != "{}" {
		var rr apiserver.AgentTaskReviewRequest
		if err := json.Unmarshal([]byte(t.ReviewRequest), &rr); err == nil {
			at.ReviewRequest = &rr
		}
	}
	if t.Deps != "" && t.Deps != "[]" {
		var deps []string
		if err := json.Unmarshal([]byte(t.Deps), &deps); err == nil && len(deps) > 0 {
			at.Deps = &deps
		}
	}
	if t.NotifyAt.Valid && t.NotifyAt.String != "" {
		notifyAt := parseTaskTime(t.NotifyAt.String)
		at.NotifyAt = &notifyAt
	}
	return at
}

// toAPITaskEvent maps a sqlc.AgentTaskEvent to the API type.
func toAPITaskEvent(e sqlc.AgentTaskEvent) apiserver.AgentTaskEvent {
	ae := apiserver.AgentTaskEvent{
		Id:        e.ID,
		TaskId:    e.TaskID,
		EventType: apitypes.AgentTaskEventEventType(e.EventType),
		CreatedAt: parseTaskTime(e.CreatedAt),
	}
	if e.Detail != "" && e.Detail != "{}" {
		var detail map[string]any
		if err := json.Unmarshal([]byte(e.Detail), &detail); err == nil && len(detail) > 0 {
			ae.Detail = &detail
		} else if err != nil {
			// Legacy plain-string detail: wrap for API compatibility.
			wrapped := map[string]any{"message": e.Detail}
			ae.Detail = &wrapped
		}
	}
	return ae
}

// parseTaskTime parses a time string stored in the tasks tables.
func parseTaskTime(s string) time.Time {
	if t := parseDBTime(s); t != nil {
		return *t
	}
	return time.Time{}
}
