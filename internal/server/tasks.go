package server

import (
	"context"
	"encoding/json"
	"errors"
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
		Id:       t.ID,
		Title:    t.Title,
		Status:   apitypes.AgentTaskStatus(t.Status),
		Priority: apitypes.AgentTaskPriority(t.Priority),
		TaskType: apitypes.AgentTaskTaskType(t.TaskType),

		CreatedAt: parseTaskTime(t.CreatedAt),
		UpdatedAt: parseTaskTime(t.UpdatedAt),
	}
	if t.Description != "" {
		at.Description = &t.Description
	}
	if t.AssigneeAgentID.Valid && t.AssigneeAgentID.String != "" {
		at.AssigneeAgentId = &t.AssigneeAgentID.String
	}
	if t.CreatedByAgentID.Valid && t.CreatedByAgentID.String != "" {
		at.CreatedByAgentId = &t.CreatedByAgentID.String
	}
	if t.ParentID.Valid && t.ParentID.String != "" {
		at.ParentId = &t.ParentID.String
	}
	at.RootId = &t.RootID
	if t.SessionID.Valid && t.SessionID.String != "" {
		at.SessionId = &t.SessionID.String
	}
	if t.UserID != "" {
		at.UserId = &t.UserID
	}
	if t.Required {
		at.Required = &t.Required
	}
	retryCount := int(t.RetryCount)
	at.RetryCount = &retryCount
	maxRetries := int(t.MaxRetries)
	at.MaxRetries = &maxRetries
	if t.ReviewPolicy.Valid {
		rp := apitypes.AgentTaskReviewPolicy(t.ReviewPolicy.String)
		at.ReviewPolicy = &rp
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
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
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
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	var body apiserver.AgentTaskBatchInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(body.Tasks) == 0 {
		writeError(w, http.StatusBadRequest, "tasks array is required")
		return
	}
	if len(body.Tasks) > 100 {
		writeError(w, http.StatusBadRequest, "too many tasks (max 100)")
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
		if errors.Is(err, tasks.ErrNotFound) || errors.Is(err, tasks.ErrCycle) {
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
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	deps, err := s.tasksSvc.GetTaskDeps(r.Context(), taskID, info.UserID)
	if err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
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
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	var body apiserver.AgentTaskDepsInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := s.tasksSvc.AddDep(r.Context(), taskID, body.DepId, info.UserID); err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
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
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	if err := s.tasksSvc.RemoveDep(r.Context(), taskID, depID, info.UserID); err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
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

func (s *Server) CreateGoal(w http.ResponseWriter, r *http.Request, agentID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}

	var body apitypes.GoalInput
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

	description := ""
	if body.Description != nil {
		description = *body.Description
	}
	priority := ""
	if body.Priority != nil {
		priority = string(*body.Priority)
	}

	goal, err := s.tasksSvc.CreateGoal(r.Context(), tasks.CreateGoalParams{
		Title:           body.Title,
		Description:     description,
		Priority:        priority,
		AssigneeAgentID: agentID,
		UserID:          info.UserID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, toAPITask(goal))
}

func (s *Server) SplitGoalIntoTasks(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	var body apitypes.SplitTaskInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if len(body.Children) > 100 {
		writeError(w, http.StatusBadRequest, "too many children (max 100)")
		return
	}

	children := make([]tasks.ChildTaskInput, 0, len(body.Children))
	for _, c := range body.Children {
		child := tasks.ChildTaskInput{
			Title:    c.Title,
			Required: true,
		}
		if c.Description != nil {
			child.Description = *c.Description
		}
		if c.Priority != nil {
			child.Priority = string(*c.Priority)
		}
		if c.AgentId != nil {
			child.AssigneeAgentID = *c.AgentId
		}
		if c.Required != nil {
			child.Required = *c.Required
		}
		if c.ReviewPolicy != nil {
			child.ReviewPolicy = string(*c.ReviewPolicy)
		}
		if c.Deps != nil {
			child.Deps = *c.Deps
		}
		if c.Criteria != nil {
			child.Criteria = *c.Criteria
		}
		children = append(children, child)
	}

	created, err := s.tasksSvc.SplitTask(r.Context(), taskID, info.UserID, children)
	if err != nil {
		if errors.Is(err, tasks.ErrNotFound) || errors.Is(err, tasks.ErrInvalidStatus) || errors.Is(err, tasks.ErrCycle) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]apiserver.AgentTask, 0, len(created))
	for _, t := range created {
		items = append(items, toAPITask(t))
	}
	writeData(w, http.StatusCreated, apiserver.AgentTaskList{Items: items})
}

func (s *Server) PlanReady(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	goal, err := s.tasksSvc.PlanReady(r.Context(), taskID, info.UserID)
	if err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, toAPITask(goal))
}

func (s *Server) ReopenAgentTask(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	task, err := s.tasksSvc.ReopenTask(r.Context(), taskID, info.UserID)
	if err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, toAPITask(task))
}

func (s *Server) ListAgentTaskRuns(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	runs, err := s.tasksSvc.ListRuns(r.Context(), taskID, info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]apitypes.AgentTaskRun, 0, len(runs))
	for _, run := range runs {
		items = append(items, toAPIRun(run))
	}
	writeData(w, http.StatusOK, apitypes.AgentTaskRunList{Items: items})
}

func (s *Server) ListAgentTaskReviews(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	reviews, err := s.tasksSvc.ListReviews(r.Context(), taskID, info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]apitypes.AgentTaskReview, 0, len(reviews))
	for _, rev := range reviews {
		items = append(items, toAPIReview(rev))
	}
	writeData(w, http.StatusOK, apitypes.AgentTaskReviewList{Items: items})
}

func (s *Server) SubmitReviewDecision(w http.ResponseWriter, r *http.Request, agentID string, taskID string, reviewID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	var body apitypes.ReviewDecisionInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	decision := tasks.ReviewDecision{
		Status:   string(body.Status),
		Summary:  derefStr(body.Summary),
		Feedback: derefStr(body.Feedback),
	}
	if body.Items != nil {
		for _, item := range *body.Items {
			ri := tasks.ReviewItemInput{
				CriterionID: item.CriterionId,
				Passed:      item.Passed,
			}
			if item.Evidence != nil {
				ri.Evidence = *item.Evidence
			}
			decision.Items = append(decision.Items, ri)
		}
	}

	task, err := s.tasksSvc.HandleReviewDecision(r.Context(), reviewID, info.UserID, decision)
	if err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, toAPITask(task))
}

func (s *Server) ListAgentTaskCriteria(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	criteria, err := s.tasksSvc.ListCriteria(r.Context(), taskID, info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]apitypes.AgentTaskAcceptanceCriterion, 0, len(criteria))
	for _, c := range criteria {
		items = append(items, toAPICriterion(c))
	}
	writeData(w, http.StatusOK, apitypes.AgentTaskAcceptanceCriterionList{Items: items})
}

func (s *Server) CreateAgentTaskCriterion(w http.ResponseWriter, r *http.Request, agentID string, taskID string) {
	if s.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not available")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	var body apitypes.AgentTaskAcceptanceCriterionInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	required := true
	if body.Required != nil {
		required = *body.Required
	}
	var position int64
	if body.Position != nil {
		position = int64(*body.Position)
	}

	criterion, err := s.tasksSvc.CreateCriterion(r.Context(), taskID, info.UserID, body.Description, required, position)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, toAPICriterion(criterion))
}

func toAPIRun(run sqlc.AgentTaskRun) apitypes.AgentTaskRun {
	r := apitypes.AgentTaskRun{
		Id:        run.ID,
		TaskId:    run.TaskID,
		Kind:      apitypes.AgentTaskRunKind(run.Kind),
		Purpose:   apitypes.AgentTaskRunPurpose(run.Purpose),
		Status:    apitypes.AgentTaskRunStatus(run.Status),
		CreatedAt: parseTaskTime(run.CreatedAt),
		UpdatedAt: parseTaskTime(run.UpdatedAt),
	}
	if run.AgentID.Valid {
		r.AgentId = &run.AgentID.String
	}
	if run.SessionID.Valid {
		r.SessionId = &run.SessionID.String
	}
	if run.ResultJson != "" && run.ResultJson != "{}" {
		var result map[string]any
		if err := json.Unmarshal([]byte(run.ResultJson), &result); err == nil && len(result) > 0 {
			r.ResultJson = &result
		}
	}
	if run.Error != "" {
		r.Error = &run.Error
	}
	if run.DeadlineAt.Valid {
		t := parseTaskTime(run.DeadlineAt.String)
		r.DeadlineAt = &t
	}
	if run.StartedAt.Valid {
		t := parseTaskTime(run.StartedAt.String)
		r.StartedAt = &t
	}
	if run.FinishedAt.Valid {
		t := parseTaskTime(run.FinishedAt.String)
		r.FinishedAt = &t
	}
	return r
}

func toAPIReview(rev sqlc.AgentTaskReview) apitypes.AgentTaskReview {
	r := apitypes.AgentTaskReview{
		Id:           rev.ID,
		TaskId:       rev.TaskID,
		ReviewerType: apitypes.AgentTaskReviewReviewerType(rev.ReviewerType),
		Status:       apitypes.AgentTaskReviewStatus(rev.Status),
		CreatedAt:    parseTaskTime(rev.CreatedAt),
	}
	if rev.ReviewerID != "" {
		r.ReviewerId = &rev.ReviewerID
	}
	if rev.SubmittedRunID != "" {
		r.SubmittedRunId = &rev.SubmittedRunID
	}
	if rev.ReviewerRunID.Valid {
		r.ReviewerRunId = &rev.ReviewerRunID.String
	}
	if rev.Summary != "" {
		r.Summary = &rev.Summary
	}
	if rev.Feedback != "" {
		r.Feedback = &rev.Feedback
	}
	if rev.ResolvedAt.Valid {
		t := parseTaskTime(rev.ResolvedAt.String)
		r.ResolvedAt = &t
	}
	return r
}

func toAPICriterion(c sqlc.AgentTaskAcceptanceCriterion) apitypes.AgentTaskAcceptanceCriterion {
	pos := int(c.Position)
	ct := parseTaskTime(c.CreatedAt)
	return apitypes.AgentTaskAcceptanceCriterion{
		Id:          c.ID,
		TaskId:      c.TaskID,
		Description: c.Description,
		Required:    c.Required,
		Position:    pos,
		CreatedAt:   &ct,
	}
}

// parseTaskTime parses a time string stored in the tasks tables.
func parseTaskTime(s string) time.Time {
	if t := parseDBTime(s); t != nil {
		return *t
	}
	return time.Time{}
}
