package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
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

// tasksReady is true when the task service has been wired at boot. Handlers
// guard on this so partially-booted servers degrade to 503 rather than
// panicking.
func (s *Server) tasksReady() bool { return s.tasksSvc != nil && s.tasksSvc.Facade != nil }

// authActor builds a tasks.Actor for an authenticated user request. Falls back
// to system if the request somehow lacks auth context.
func authActor(r *http.Request) tasks.Actor {
	if info := UserFromContext(r.Context()); info != nil && info.UserID != "" {
		return tasks.Actor{Type: tasks.ActorUser, ID: info.UserID}
	}
	return tasks.SystemActor()
}

// taskError centralizes the error → HTTP mapping (D4).
func (s *Server) taskError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tasks.ErrTaskNotFound), errors.Is(err, tasks.ErrBlockerNotFound), errors.Is(err, tasks.ErrReviewNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, tasks.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "invalid_transition")
	case errors.Is(err, tasks.ErrCycle):
		writeError(w, http.StatusConflict, "dep_cycle")
	case errors.Is(err, tasks.ErrReviewClosed):
		writeError(w, http.StatusConflict, "review_closed")
	case errors.Is(err, tasks.ErrDepFailureUnresolved):
		writeError(w, http.StatusConflict, "dep_failure_requires_waiver")
	case errors.Is(err, tasks.ErrAlreadyClosed):
		writeError(w, http.StatusConflict, "blocker_already_closed")
	case errors.Is(err, tasks.ErrUnsupportedReviewPolicy):
		writeError(w, http.StatusBadRequest, "unsupported_review_policy")
	case errors.Is(err, tasks.ErrInvalidPlanMode):
		writeError(w, http.StatusBadRequest, "invalid_plan_mode")
	case errors.Is(err, tasks.ErrInvalidTaskContext):
		writeError(w, http.StatusBadRequest, "invalid_task_context")
	case errors.Is(err, tasks.ErrPlanMaterializationRequired):
		// Goal work tasks come only from a materialized plan; a goal_id on a
		// public task create is rejected here (#525).
		writeError(w, http.StatusBadRequest, "goal_tasks_require_plan")
	case errors.Is(err, tasks.ErrAcceptedPlanRequired):
		writeError(w, http.StatusConflict, "goal_plan_not_accepted")
	case errors.Is(err, tasks.ErrPlanNotMaterialized):
		writeError(w, http.StatusConflict, "goal_plan_not_materialized")
	case errors.Is(err, tasks.ErrInvalidPlan):
		writeError(w, http.StatusBadRequest, "invalid_plan")
	case errors.Is(err, tasks.ErrGoalPlanNotFound):
		writeError(w, http.StatusNotFound, "goal_plan_not_found")
	case errors.Is(err, tasks.ErrPlanReviewExists):
		writeError(w, http.StatusConflict, "plan_review_exists")
	case errors.Is(err, tasks.ErrNoPendingPlanEdit):
		writeError(w, http.StatusConflict, "no_pending_plan_edit")
	case errors.Is(err, tasks.ErrPlanNotUnderReview):
		writeError(w, http.StatusConflict, "plan_not_under_review")
	case errors.Is(err, tasks.ErrPlanReviewWrongPath):
		writeError(w, http.StatusConflict, "plan_review_wrong_path")
	case errors.Is(err, tasks.ErrPlanReviewPolicyMismatch):
		writeError(w, http.StatusConflict, "plan_review_policy_mismatch")
	case errors.Is(err, tasks.ErrPlanItemInFlight):
		writeError(w, http.StatusConflict, "plan_item_in_flight")
	case errors.Is(err, tasks.ErrPlanChangedDuringMaterialize):
		writeError(w, http.StatusConflict, "plan_changed_retry")
	default:
		var conflict *tasks.ErrReopenConflict
		if errors.As(err, &conflict) {
			writeErrorDetails(w, http.StatusConflict, "reopen_conflict", map[string]any{
				"downstream_ids": conflict.DownstreamIDs,
			})
			return
		}
		s.writeInternalError(w, err)
	}
}

// loadTask resolves a {taskID} path param to a task row owned by the current
// user. Other users' tasks are reported as 404 to avoid leaking existence.
// Returns false (and writes an error) on miss or ownership mismatch.
func (s *Server) loadTask(ctx context.Context, w http.ResponseWriter, taskID string) (sqlc.AgentTask, bool) {
	t, err := s.tasksSvc.Facade.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, tasks.ErrTaskNotFound) || errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found")
			return sqlc.AgentTask{}, false
		}
		s.taskError(w, err)
		return sqlc.AgentTask{}, false
	}
	info := UserFromContext(ctx)
	if info == nil || info.UserID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return sqlc.AgentTask{}, false
	}
	if t.UserID != info.UserID {
		writeError(w, http.StatusNotFound, "not_found")
		return sqlc.AgentTask{}, false
	}
	if info.Scoped != nil && t.AgentID != info.Scoped.AgentID {
		writeError(w, http.StatusForbidden, "permission denied")
		return sqlc.AgentTask{}, false
	}
	return t, true
}

// ---------------------------------------------------------------------------
// List / create / get
// ---------------------------------------------------------------------------

func (s *Server) ListTasks(w http.ResponseWriter, r *http.Request, params apiserver.ListTasksParams) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if info.UserID == "" {
		writeError(w, http.StatusUnauthorized, "missing_user")
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	agentID := ""
	if params.AgentId != nil {
		agentID = *params.AgentId
	}
	if info.Scoped != nil {
		if agentID != "" && agentID != info.Scoped.AgentID {
			writeError(w, http.StatusForbidden, "permission denied")
			return
		}
		agentID = info.Scoped.AgentID
	}
	projectID := ""
	if params.ProjectId != nil {
		projectID = *params.ProjectId
	}
	status := ""
	if params.Status != nil {
		status = *params.Status
	}
	archived := false
	if params.Archived != nil {
		archived = *params.Archived
	}
	rows, err := s.tasksSvc.Facade.ListTasksByUser(r.Context(), info.UserID, tasks.TaskFilter{
		AgentID:   agentID,
		ProjectID: projectID,
		Status:    status,
		Archived:  archived,
	}, int64(limit+1), int64(offset))
	if err != nil {
		s.taskError(w, err)
		return
	}
	rows, nextToken := nextPageTokenForRows(rows, limit, offset)
	out := make([]apitypes.Task, 0, len(rows))
	for _, t := range rows {
		out = append(out, taskToAPI(t))
	}
	list := apitypes.TaskList{Tasks: out}
	if nextToken != "" {
		list.NextPageToken = &nextToken
	}
	writeData(w, http.StatusOK, list)
}

func (s *Server) CreateTask(w http.ResponseWriter, r *http.Request) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if info.UserID == "" {
		writeError(w, http.StatusUnauthorized, "missing_user")
		return
	}
	var req apitypes.CreateTaskRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title_required")
		return
	}

	agentID := strPtr(req.AgentId)
	if info.Scoped != nil {
		if agentID != "" && agentID != info.Scoped.AgentID {
			writeError(w, http.StatusForbidden, "permission denied")
			return
		}
		agentID = info.Scoped.AgentID
		req.AgentId = &agentID
	}
	goalID := strPtr(req.GoalId)
	in := tasks.CreateTaskInput{
		UserID:          info.UserID,
		Title:           req.Title,
		Description:     strPtr(req.Description),
		AgentID:         agentID,
		GoalID:          goalID,
		ProjectID:       strPtr(req.ProjectId),
		ExecutorAgentID: strPtr(req.ExecutorAgentId),
		Required:        req.Required,
		Context:         marshalContext(req.Context),
	}
	if req.Priority != nil {
		in.Priority = string(*req.Priority)
	}
	if req.MaxRetries != nil {
		in.MaxRetries = *req.MaxRetries
	}
	if req.NotBefore != nil {
		in.NotBefore = *req.NotBefore
	}
	if req.DeadlineAt != nil {
		in.DeadlineAt = *req.DeadlineAt
	}
	if req.ActivateOnCreate != nil {
		in.ActivateOnCreate = *req.ActivateOnCreate
	}
	if req.Deps != nil {
		for _, d := range *req.Deps {
			di := tasks.DepInput{DepTaskID: d.DepTaskId}
			if d.Kind != nil {
				di.Kind = string(*d.Kind)
			}
			if d.OnFailure != nil {
				di.OnFailure = string(*d.OnFailure)
			}
			in.Deps = append(in.Deps, di)
		}
	}
	t, err := s.tasksSvc.Facade.CreateTask(r.Context(), in)
	if err != nil {
		s.taskError(w, err)
		return
	}
	writeData(w, http.StatusCreated, taskToAPI(t))
}

func (s *Server) GetTask(w http.ResponseWriter, r *http.Request, taskID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	writeData(w, http.StatusOK, taskToAPI(t))
}

func (s *Server) DeleteTask(w http.ResponseWriter, r *http.Request, taskID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	if err := s.tasksSvc.Facade.ArchiveTask(r.Context(), t.ID, authActor(r)); err != nil {
		s.taskError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) UnarchiveTask(w http.ResponseWriter, r *http.Request, taskID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	if err := s.tasksSvc.Facade.UnarchiveTask(r.Context(), t.ID, authActor(r)); err != nil {
		s.taskError(w, err)
		return
	}
	fresh, err := s.tasksSvc.Facade.GetTask(r.Context(), t.ID)
	if err != nil {
		s.taskError(w, err)
		return
	}
	writeData(w, http.StatusOK, taskToAPI(fresh))
}

// ---------------------------------------------------------------------------
// Cancel / reopen
// ---------------------------------------------------------------------------

func (s *Server) CancelTask(w http.ResponseWriter, r *http.Request, taskID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	var req apitypes.CancelRequest
	_ = decodeJSON(r, &req) // body is optional
	if err := s.tasksSvc.Facade.CancelTask(r.Context(), t.ID, strPtr(req.Reason), authActor(r)); err != nil {
		s.taskError(w, err)
		return
	}
	fresh, err := s.tasksSvc.Facade.GetTask(r.Context(), t.ID)
	if err != nil {
		s.taskError(w, err)
		return
	}
	writeData(w, http.StatusOK, taskToAPI(fresh))
}

func (s *Server) ReopenTask(w http.ResponseWriter, r *http.Request, taskID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	var req apitypes.ReopenRequest
	_ = decodeJSON(r, &req)
	cascade := req.Cascade != nil && *req.Cascade
	if err := s.tasksSvc.Facade.ReopenTask(r.Context(), t.ID, cascade, authActor(r)); err != nil {
		s.taskError(w, err)
		return
	}
	fresh, err := s.tasksSvc.Facade.GetTask(r.Context(), t.ID)
	if err != nil {
		s.taskError(w, err)
		return
	}
	writeData(w, http.StatusOK, taskToAPI(fresh))
}

// ---------------------------------------------------------------------------
// Readiness / events / runs
// ---------------------------------------------------------------------------

func (s *Server) GetTaskReadiness(w http.ResponseWriter, r *http.Request, taskID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	rd, err := s.tasksSvc.Facade.GetReadiness(r.Context(), t.ID)
	if err != nil {
		s.taskError(w, err)
		return
	}
	writeData(w, http.StatusOK, readinessToAPI(rd))
}

func (s *Server) ListTaskEvents(w http.ResponseWriter, r *http.Request, taskID string, params apiserver.ListTaskEventsParams) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	rows, err := s.tasksSvc.Facade.ListEvents(r.Context(), t.ID, int64(limit+1), int64(offset))
	if err != nil {
		s.taskError(w, err)
		return
	}
	rows, nextToken := nextPageTokenForRows(rows, limit, offset)
	out := make([]apitypes.Event, 0, len(rows))
	for _, e := range rows {
		out = append(out, eventToAPI(e))
	}
	list := apitypes.EventList{Events: out}
	if nextToken != "" {
		list.NextPageToken = &nextToken
	}
	writeData(w, http.StatusOK, list)
}

func (s *Server) ListTaskRuns(w http.ResponseWriter, r *http.Request, taskID string, params apiserver.ListTaskRunsParams) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	rows, err := s.tasksSvc.Facade.ListRuns(r.Context(), t.ID, int64(limit+1), int64(offset))
	if err != nil {
		s.taskError(w, err)
		return
	}
	rows, nextToken := nextPageTokenForRows(rows, limit, offset)
	out := make([]apitypes.Run, 0, len(rows))
	for _, run := range rows {
		out = append(out, runToAPI(run))
	}
	list := apitypes.RunList{Runs: out}
	if nextToken != "" {
		list.NextPageToken = &nextToken
	}
	writeData(w, http.StatusOK, list)
}

// ---------------------------------------------------------------------------
// Deps
// ---------------------------------------------------------------------------

func (s *Server) ListTaskDeps(w http.ResponseWriter, r *http.Request, taskID string, params apiserver.ListTaskDepsParams) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	rows, err := s.tasksSvc.Facade.ListDeps(r.Context(), t.ID, int64(limit+1), int64(offset))
	if err != nil {
		s.taskError(w, err)
		return
	}
	rows, nextToken := nextPageTokenForRows(rows, limit, offset)
	out := make([]apitypes.Dep, 0, len(rows))
	for _, row := range rows {
		out = append(out, depToAPI(row))
	}
	list := apitypes.DepList{Deps: out}
	if nextToken != "" {
		list.NextPageToken = &nextToken
	}
	writeData(w, http.StatusOK, list)
}

func (s *Server) AddTaskDep(w http.ResponseWriter, r *http.Request, taskID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	var req apitypes.AddDepRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if req.DepTaskId == "" {
		writeError(w, http.StatusBadRequest, "dep_task_id_required")
		return
	}
	dep, ok := s.loadTask(r.Context(), w, req.DepTaskId)
	if !ok {
		return
	}
	kind := ""
	if req.Kind != nil {
		kind = string(*req.Kind)
	}
	onFailure := ""
	if req.OnFailure != nil {
		onFailure = string(*req.OnFailure)
	}
	if err := s.tasksSvc.Facade.AddDep(r.Context(), t.ID, dep.ID, kind, onFailure); err != nil {
		s.taskError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) WaiveTaskDep(w http.ResponseWriter, r *http.Request, taskID string, depTaskID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	info := UserFromContext(r.Context())
	userID := ""
	if info != nil {
		userID = info.UserID
	}
	if _, ok := s.loadTask(r.Context(), w, depTaskID); !ok {
		return
	}
	var req apitypes.WaiveDepRequest
	_ = decodeJSON(r, &req)
	if err := s.tasksSvc.Facade.WaiveDep(r.Context(), t.ID, depTaskID, userID, strPtr(req.Reason), authActor(r)); err != nil {
		s.taskError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Blockers
// ---------------------------------------------------------------------------

func (s *Server) GetTaskBlocker(w http.ResponseWriter, r *http.Request, taskID string, blockerID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	blocker, err := s.tasksSvc.Facade.GetBlocker(r.Context(), blockerID)
	if err != nil || blocker.TaskID != t.ID {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	writeData(w, http.StatusOK, blockerToAPI(blocker))
}

func blockerToAPI(b sqlc.AgentTaskBlocker) apitypes.Blocker {
	out := apitypes.Blocker{
		Id:         b.ID,
		TaskId:     b.TaskID,
		Kind:       apitypes.BlockerKind(b.Kind),
		Status:     apitypes.BlockerStatus(b.Status),
		Question:   b.Question,
		Detail:     optStr(b.Detail),
		Resolution: optStr(b.Resolution),
		CreatedAt:  parseTS(b.CreatedAt),
	}
	if b.ResolvedAt.Valid {
		v := parseTS(b.ResolvedAt.String)
		out.ResolvedAt = &v
	}
	return out
}

func (s *Server) ResolveTaskBlocker(w http.ResponseWriter, r *http.Request, taskID string, blockerID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	blocker, err := s.tasksSvc.Facade.GetBlocker(r.Context(), blockerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if blocker.TaskID != t.ID {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var req apitypes.ResolveBlockerRequest
	_ = decodeJSON(r, &req)
	if err := s.tasksSvc.Facade.ResolveBlocker(r.Context(), blockerID, strPtr(req.Resolution), authActor(r)); err != nil {
		s.taskError(w, err)
		return
	}
	fresh, err := s.tasksSvc.Facade.GetTask(r.Context(), t.ID)
	if err != nil {
		s.taskError(w, err)
		return
	}
	writeData(w, http.StatusOK, taskToAPI(fresh))
}

// ---------------------------------------------------------------------------
// Reviews
// ---------------------------------------------------------------------------

func (s *Server) ListTaskReviews(w http.ResponseWriter, r *http.Request, taskID string, params apiserver.ListTaskReviewsParams) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	rows, err := s.tasksSvc.Facade.ListReviews(r.Context(), t.ID, int64(limit+1), int64(offset))
	if err != nil {
		s.taskError(w, err)
		return
	}
	rows, nextToken := nextPageTokenForRows(rows, limit, offset)
	out := make([]apitypes.Review, 0, len(rows))
	for _, rev := range rows {
		out = append(out, reviewToAPI(rev))
	}
	list := apitypes.ReviewList{Reviews: out}
	if nextToken != "" {
		list.NextPageToken = &nextToken
	}
	writeData(w, http.StatusOK, list)
}

func (s *Server) ApproveTaskReview(w http.ResponseWriter, r *http.Request, taskID string, reviewID string) {
	s.decideReview(w, r, taskID, reviewID, "approve")
}

func (s *Server) RejectTaskReview(w http.ResponseWriter, r *http.Request, taskID string, reviewID string) {
	s.decideReview(w, r, taskID, reviewID, "reject")
}

func (s *Server) RequestChangesTaskReview(w http.ResponseWriter, r *http.Request, taskID string, reviewID string) {
	s.decideReview(w, r, taskID, reviewID, "request_changes")
}

func (s *Server) EscalateTaskReview(w http.ResponseWriter, r *http.Request, taskID string, reviewID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	rev, err := s.tasksSvc.Facade.GetReview(r.Context(), reviewID)
	if err != nil || !rev.TaskID.Valid || rev.TaskID.String != t.ID {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var req apitypes.EscalateReviewRequest
	_ = decodeJSON(r, &req)
	if err := s.tasksSvc.Facade.EscalateReview(r.Context(), reviewID, strPtr(req.Reason), authActor(r)); err != nil {
		s.taskError(w, err)
		return
	}
	fresh, err := s.tasksSvc.Facade.GetReview(r.Context(), reviewID)
	if err != nil {
		// The escalation created a new review row; refetch via task list.
		writeData(w, http.StatusOK, reviewToAPI(rev))
		return
	}
	writeData(w, http.StatusOK, reviewToAPI(fresh))
}

// decideReview is the shared body for approve / reject / request_changes.
func (s *Server) decideReview(w http.ResponseWriter, r *http.Request, taskID, reviewID, kind string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	t, ok := s.loadTask(r.Context(), w, taskID)
	if !ok {
		return
	}
	rev, err := s.tasksSvc.Facade.GetReview(r.Context(), reviewID)
	if err != nil || !rev.TaskID.Valid || rev.TaskID.String != t.ID {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var req apitypes.ReviewDecisionRequest
	_ = decodeJSON(r, &req)
	actor := authActor(r)
	switch kind {
	case "approve":
		err = s.tasksSvc.Facade.ApproveReview(r.Context(), reviewID, strPtr(req.Summary), actor)
	case "reject":
		err = s.tasksSvc.Facade.RejectReview(r.Context(), reviewID, strPtr(req.Summary), strPtr(req.Feedback), actor)
	case "request_changes":
		err = s.tasksSvc.Facade.RequestChanges(r.Context(), reviewID, strPtr(req.Summary), strPtr(req.Feedback), actor)
	}
	if err != nil {
		s.taskError(w, err)
		return
	}
	fresh, gerr := s.tasksSvc.Facade.GetReview(r.Context(), reviewID)
	if gerr != nil {
		writeData(w, http.StatusOK, reviewToAPI(rev))
		return
	}
	writeData(w, http.StatusOK, reviewToAPI(fresh))
}

// ---------------------------------------------------------------------------
// sqlc → API mapping helpers
// ---------------------------------------------------------------------------

func taskToAPI(t sqlc.AgentTask) apitypes.Task {
	out := apitypes.Task{
		Id:           t.ID,
		UserId:       t.UserID,
		Title:        t.Title,
		Description:  optStr(t.Description),
		Status:       apitypes.TaskStatus(t.Status),
		Priority:     apitypes.TaskPriority(t.Priority),
		ReviewPolicy: apitypes.TaskReviewPolicy(t.ReviewPolicy),
		Required:     t.Required != 0,
		RetryCount:   t.RetryCount,
		MaxRetries:   t.MaxRetries,
		CreatedAt:    parseTS(t.CreatedAt),
		UpdatedAt:    parseTS(t.UpdatedAt),
	}
	out.AgentId = t.AgentID
	if t.GoalID.Valid {
		v := t.GoalID.String
		out.GoalId = &v
	}
	if t.ProjectID.Valid {
		v := t.ProjectID.String
		out.ProjectId = &v
	}
	if t.NotBefore.Valid {
		v := parseTS(t.NotBefore.String)
		out.NotBefore = &v
	}
	if t.DeadlineAt.Valid {
		v := parseTS(t.DeadlineAt.String)
		out.DeadlineAt = &v
	}
	out.SessionId = t.SessionID
	if t.ActiveRunID.Valid {
		v := t.ActiveRunID.String
		out.ActiveRunId = &v
	}
	if t.ActiveBlockerID.Valid {
		v := t.ActiveBlockerID.String
		out.ActiveBlockerId = &v
	}
	if t.ActiveReviewID.Valid {
		v := t.ActiveReviewID.String
		out.ActiveReviewId = &v
	}
	if t.CompletedAt.Valid {
		v := parseTS(t.CompletedAt.String)
		out.CompletedAt = &v
	}
	if t.CancelledAt.Valid {
		v := parseTS(t.CancelledAt.String)
		out.CancelledAt = &v
	}
	if t.ArchivedAt.Valid {
		v := parseTS(t.ArchivedAt.String)
		out.ArchivedAt = &v
	}
	if m := jsonObject(t.Context); m != nil {
		out.Context = m
	}
	if m := jsonObject(t.Output); m != nil {
		out.Output = m
	}
	return out
}

func runToAPI(r sqlc.AgentTaskRun) apitypes.Run {
	out := apitypes.Run{
		Id:        r.ID,
		TaskId:    r.TaskID.String,
		UserId:    r.UserID,
		Kind:      apitypes.RunKind(r.Kind),
		AttemptNo: r.AttemptNo,
		Status:    apitypes.RunStatus(r.Status),
		SessionId: r.SessionID,
		CreatedAt: parseTS(r.CreatedAt),
		UpdatedAt: parseTS(r.UpdatedAt),
	}
	if r.AgentID.Valid {
		v := r.AgentID.String
		out.AgentId = &v
	}
	if r.ExecutorAgentID.Valid {
		v := r.ExecutorAgentID.String
		out.ExecutorAgentId = &v
	}
	if r.Input != "" {
		v := r.Input
		out.Input = &v
	}
	if r.Result != "" {
		v := r.Result
		out.Result = &v
	}
	if r.Error != "" {
		v := r.Error
		out.Error = &v
	}
	if r.WorkerID != "" {
		v := r.WorkerID
		out.WorkerId = &v
	}
	if r.HeartbeatAt.Valid {
		v := parseTS(r.HeartbeatAt.String)
		out.HeartbeatAt = &v
	}
	if r.LeaseExpiresAt.Valid {
		v := parseTS(r.LeaseExpiresAt.String)
		out.LeaseExpiresAt = &v
	}
	if r.StartedAt.Valid {
		v := parseTS(r.StartedAt.String)
		out.StartedAt = &v
	}
	if r.FinishedAt.Valid {
		v := parseTS(r.FinishedAt.String)
		out.FinishedAt = &v
	}
	return out
}

func eventToAPI(e sqlc.AgentTaskEvent) apitypes.Event {
	out := apitypes.Event{
		Id:        e.ID,
		EventType: e.EventType,
		ActorType: apitypes.EventActorType(e.ActorType),
		CreatedAt: parseTS(e.CreatedAt),
	}
	if e.TaskID.Valid {
		v := e.TaskID.String
		out.TaskId = &v
	}
	if e.RunID.Valid {
		v := e.RunID.String
		out.RunId = &v
	}
	if e.ReviewID.Valid {
		v := e.ReviewID.String
		out.ReviewId = &v
	}
	if e.FromStatus.Valid {
		v := e.FromStatus.String
		out.FromStatus = &v
	}
	if e.ToStatus.Valid {
		v := e.ToStatus.String
		out.ToStatus = &v
	}
	if e.ActorID.Valid {
		v := e.ActorID.String
		out.ActorId = &v
	}
	if m := jsonObject(e.Detail); m != nil {
		out.Detail = m
	}
	return out
}

func depToAPI(row sqlc.ListAgentTaskDepsWithUpstreamPagedRow) apitypes.Dep {
	out := apitypes.Dep{
		TaskId:    row.AgentTaskDep.TaskID,
		DepTaskId: row.AgentTaskDep.DepTaskID,
		DepKind:   apitypes.DepDepKind(row.AgentTaskDep.DepKind),
		OnFailure: apitypes.DepOnFailure(row.AgentTaskDep.OnFailure),
		CreatedAt: parseTS(row.AgentTaskDep.CreatedAt),
	}
	if row.UpstreamStatus != "" {
		v := row.UpstreamStatus
		out.UpstreamStatus = &v
	}
	if row.AgentTaskDep.WaivedAt.Valid {
		v := parseTS(row.AgentTaskDep.WaivedAt.String)
		out.WaivedAt = &v
	}
	if row.AgentTaskDep.WaivedByUser.Valid {
		v := row.AgentTaskDep.WaivedByUser.String
		out.WaivedBy = &v
	}
	if row.AgentTaskDep.WaiverReason != "" {
		v := row.AgentTaskDep.WaiverReason
		out.WaiverReason = &v
	}
	return out
}

func reviewToAPI(rev sqlc.AgentReview) apitypes.Review {
	out := apitypes.Review{
		Id:           rev.ID,
		ReviewerType: apitypes.ReviewReviewerType(rev.ReviewerType),
		Status:       apitypes.ReviewStatus(rev.Status),
		Summary:      rev.Summary,
		Feedback:     rev.Feedback,
		CreatedAt:    parseTS(rev.CreatedAt),
		UpdatedAt:    parseTS(rev.UpdatedAt),
	}
	if rev.TaskID.Valid {
		v := rev.TaskID.String
		out.TaskId = &v
	}
	if rev.GoalID.Valid {
		v := rev.GoalID.String
		out.GoalId = &v
	}
	if rev.SubmittedRunID.Valid {
		v := rev.SubmittedRunID.String
		out.SubmittedRunId = &v
	}
	if rev.ReviewerRunID.Valid {
		v := rev.ReviewerRunID.String
		out.ReviewerRunId = &v
	}
	if rev.ReviewerUserID.Valid {
		v := rev.ReviewerUserID.String
		out.ReviewerUserId = &v
	}
	if rev.EscalatedFromReviewID.Valid {
		v := rev.EscalatedFromReviewID.String
		out.EscalatedFromReviewId = &v
	}
	if rev.ResolvedAt.Valid {
		v := parseTS(rev.ResolvedAt.String)
		out.ResolvedAt = &v
	}
	return out
}

func readinessToAPI(rd tasks.Readiness) apitypes.Readiness {
	out := apitypes.Readiness{
		State:        apitypes.ReadinessState(rd.State),
		Dispatchable: rd.Dispatchable,
	}
	if len(rd.Reasons) > 0 {
		reasons := make([]apitypes.ReadinessReason, 0, len(rd.Reasons))
		for _, r := range rd.Reasons {
			rr := apitypes.ReadinessReason{Type: r.Type}
			if r.UpstreamID != "" {
				v := r.UpstreamID
				rr.UpstreamId = &v
			}
			if r.Detail != "" {
				v := r.Detail
				rr.Detail = &v
			}
			reasons = append(reasons, rr)
		}
		out.Reasons = &reasons
	}
	return out
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func strPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func optStr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// parseTS parses sqlc-emitted timestamps (RFC3339Nano or SQLite-style).
func parseTS(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func jsonObject(s string) *map[string]any {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return &m
}

// marshalContext takes the API CreateTaskRequest.Context map and encodes it
// as a JSON string for storage. Returns "{}" on nil.
func marshalContext(m *map[string]any) string {
	if m == nil {
		return ""
	}
	b, err := json.Marshal(*m)
	if err != nil {
		return ""
	}
	return string(b)
}
