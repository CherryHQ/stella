package server

import (
	"context"
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

func (s *Server) ListAgentTasks(w http.ResponseWriter, r *http.Request, agentID string, params apiserver.ListAgentTasksParams) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())
	var userID string
	if info != nil {
		userID = info.UserID
	}

	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	var statusFilter string
	if params.Status != nil {
		statusFilter = *params.Status
	}

	list, err := s.tasksSvc.ListTasks(r.Context(), userID, agentID, statusFilter)
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

func (s *Server) CreateAgentTask(w http.ResponseWriter, r *http.Request, agentID string) {
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
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	var userID string
	if info != nil {
		userID = info.UserID
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
		Title:           body.Title,
		Description:     description,
		Priority:        priority,
		AssigneeAgentID: agentID,
		UserID:          userID,
		Deps:            deps,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, toAPITask(task))
}

func (s *Server) GetAgentTask(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())
	var userID string
	if info != nil {
		userID = info.UserID
	}

	task, err := s.tasksSvc.GetTask(r.Context(), taskID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	if info != nil && task.UserID != info.UserID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	if !task.AssigneeAgentID.Valid || task.AssigneeAgentID.String != agentID {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeData(w, http.StatusOK, toAPITask(task))
}

func (s *Server) UpdateAgentTask(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())
	var userID string
	if info != nil {
		userID = info.UserID
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
	existing, err := s.tasksSvc.GetTask(r.Context(), taskID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if !existing.AssigneeAgentID.Valid || existing.AssigneeAgentID.String != agentID {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	task, err := s.tasksSvc.UpdateTask(r.Context(), taskID, userID, tasks.UpdateTaskParams{
		Title:       title,
		Description: description,
		Priority:    priority,
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

func (s *Server) DeleteAgentTask(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())
	var userID string
	if info != nil {
		userID = info.UserID
	}

	task, err := s.tasksSvc.GetTask(r.Context(), taskID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if !task.AssigneeAgentID.Valid || task.AssigneeAgentID.String != agentID {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	if err := s.tasksSvc.DeleteTask(r.Context(), taskID, userID); err != nil {
		if strings.Contains(err.Error(), "forbidden") {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeNoContent(w)
}

func (s *Server) AgentTaskAction(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())
	var userID string
	if info != nil {
		userID = info.UserID
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

	existing, err := s.tasksSvc.GetTask(r.Context(), taskID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if !existing.AssigneeAgentID.Valid || existing.AssigneeAgentID.String != agentID {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	task, err := s.tasksSvc.HandleAction(r.Context(), taskID, userID, tasks.ActionParams{
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

func (s *Server) ListAgentTaskEvents(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())
	var userID string
	if info != nil {
		userID = info.UserID
	}

	task, err := s.tasksSvc.GetTask(r.Context(), taskID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if info != nil && task.UserID != info.UserID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	if !task.AssigneeAgentID.Valid || task.AssigneeAgentID.String != agentID {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	events, err := s.tasksSvc.ListTaskEvents(r.Context(), taskID)
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
	if t.AssigneeAgentID.Valid && t.AssigneeAgentID.String != "" {
		at.AgentId = &t.AssigneeAgentID.String
	}
	if t.SessionID.Valid && t.SessionID.String != "" {
		at.SessionId = &t.SessionID.String
	}
	if t.UserID != "" {
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
	// deps are now populated separately from the edge table
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
		}
	}
	return ae
}

// toAPITaskWithDeps maps a sqlc.AgentTask to the API type and populates deps from edge table.
func (s *Server) toAPITaskWithDeps(ctx context.Context, t sqlc.AgentTask) apiserver.AgentTask {
	at := toAPITask(t)
	if s.tasksSvc != nil {
		if deps, err := s.tasksSvc.ListTaskDeps(ctx, t.ID); err == nil && len(deps) > 0 {
			at.Deps = &deps
		}
	}
	return at
}

func (s *Server) ListUnblockedAgentTasks(w http.ResponseWriter, r *http.Request, agentID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())
	list, err := s.tasksSvc.ListUnblockedTasks(r.Context(), info.UserID, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]apiserver.AgentTask, 0, len(list))
	for _, t := range list {
		items = append(items, s.toAPITaskWithDeps(r.Context(), t))
	}
	writeData(w, http.StatusOK, apiserver.AgentTaskList{Items: items})
}

func (s *Server) BatchCreateAgentTasks(w http.ResponseWriter, r *http.Request, agentID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())

	var body apiserver.AgentTaskBatchInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(body.Tasks) == 0 {
		writeError(w, http.StatusBadRequest, "tasks array is required")
		return
	}

	batchItems := make([]tasks.BatchTaskItem, 0, len(body.Tasks))
	for _, t := range body.Tasks {
		item := tasks.BatchTaskItem{Title: t.Title}
		if t.Description != nil {
			item.Description = *t.Description
		}
		if t.Priority != nil {
			item.Priority = string(*t.Priority)
		}
		if t.AgentId != nil {
			item.AssigneeAgentID = *t.AgentId
		}
		if t.DraftId != nil {
			item.DraftID = *t.DraftId
		}
		if t.Deps != nil {
			item.Deps = *t.Deps
		}
		batchItems = append(batchItems, item)
	}

	created, err := s.tasksSvc.BatchCreateTasks(r.Context(), tasks.BatchCreateParams{
		UserID: info.UserID,
		Tasks:  batchItems,
	})
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "cycle") || strings.Contains(err.Error(), "duplicate") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]apiserver.AgentTask, 0, len(created))
	for _, t := range created {
		items = append(items, s.toAPITaskWithDeps(r.Context(), t))
	}
	writeData(w, http.StatusCreated, apiserver.AgentTaskList{Items: items})
}

func (s *Server) GetAgentTaskDeps(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())

	deps, err := s.tasksSvc.GetTaskDeps(r.Context(), taskID, info.UserID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	upstream := make([]apiserver.AgentTask, 0, len(deps.Upstream))
	for _, t := range deps.Upstream {
		upstream = append(upstream, s.toAPITaskWithDeps(r.Context(), t))
	}
	downstream := make([]apiserver.AgentTask, 0, len(deps.Downstream))
	for _, t := range deps.Downstream {
		downstream = append(downstream, s.toAPITaskWithDeps(r.Context(), t))
	}
	writeData(w, http.StatusOK, apiserver.AgentTaskDepsInfo{
		Upstream:   upstream,
		Downstream: downstream,
	})
}

func (s *Server) AddAgentTaskDep(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())

	var body apiserver.AgentTaskDepsInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := s.tasksSvc.AddDep(r.Context(), taskID, body.DepId, info.UserID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	task, err := s.tasksSvc.GetTask(r.Context(), taskID, info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, s.toAPITaskWithDeps(r.Context(), task))
}

func (s *Server) RemoveAgentTaskDep(w http.ResponseWriter, r *http.Request, agentID string, taskID string, depID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := UserFromContext(r.Context())

	if err := s.tasksSvc.RemoveDep(r.Context(), taskID, depID, info.UserID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	task, err := s.tasksSvc.GetTask(r.Context(), taskID, info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, s.toAPITaskWithDeps(r.Context(), task))
}

// parseTaskTime parses a time string stored in the tasks tables.
func parseTaskTime(s string) time.Time {
	if t := parseDBTime(s); t != nil {
		return *t
	}
	return time.Time{}
}
