package server

import (
	"context"
	"errors"
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/tasks"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// loadGoal fetches a goal by id. Returns false (after writing 404) on miss.
func (s *Server) loadGoal(ctx context.Context, w http.ResponseWriter, goalID string) (sqlc.AgentGoal, bool) {
	g, err := s.tasksSvc.Facade.GetGoal(ctx, goalID)
	if err != nil {
		if errors.Is(err, tasks.ErrGoalNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return sqlc.AgentGoal{}, false
		}
		taskError(w, err)
		return sqlc.AgentGoal{}, false
	}
	return g, true
}

// ListGoals returns all goals.
func (s *Server) ListGoals(w http.ResponseWriter, r *http.Request, params apiserver.ListGoalsParams) {
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
	rows, err := s.tasksSvc.Facade.ListGoals(r.Context(), int64(limit+1), int64(offset))
	if err != nil {
		taskError(w, err)
		return
	}
	rows, nextToken := nextPageTokenForRows(rows, limit, offset)
	out := make([]apitypes.Goal, 0, len(rows))
	for _, g := range rows {
		out = append(out, goalToAPI(g))
	}
	list := apitypes.GoalList{Goals: out}
	if nextToken != "" {
		list.NextPageToken = &nextToken
	}
	writeData(w, http.StatusOK, list)
}

func (s *Server) CreateGoal(w http.ResponseWriter, r *http.Request) {
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
	var req apitypes.CreateGoalRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title_required")
		return
	}
	in := tasks.CreateGoalInput{
		UserID:      info.UserID,
		Title:       req.Title,
		Description: strPtr(req.Description),
		AgentID:     strPtr(req.AgentId),
		Context:     marshalContext(req.Context),
	}
	if req.Priority != nil {
		in.Priority = string(*req.Priority)
	}
	if req.ReviewPolicy != nil {
		in.ReviewPolicy = string(*req.ReviewPolicy)
	}
	g, err := s.tasksSvc.Facade.CreateGoal(r.Context(), in)
	if err != nil {
		taskError(w, err)
		return
	}
	writeData(w, http.StatusCreated, goalToAPI(g))
}

func (s *Server) GetGoal(w http.ResponseWriter, r *http.Request, goalID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	g, ok := s.loadGoal(r.Context(), w, goalID)
	if !ok {
		return
	}
	writeData(w, http.StatusOK, goalToAPI(g))
}

func (s *Server) ActivateGoal(w http.ResponseWriter, r *http.Request, goalID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	g, ok := s.loadGoal(r.Context(), w, goalID)
	if !ok {
		return
	}
	if err := s.tasksSvc.Facade.ActivateGoal(r.Context(), g.ID, authActor(r)); err != nil {
		taskError(w, err)
		return
	}
	fresh, err := s.tasksSvc.Facade.GetGoal(r.Context(), g.ID)
	if err != nil {
		taskError(w, err)
		return
	}
	writeData(w, http.StatusOK, goalToAPI(fresh))
}

func (s *Server) CancelGoal(w http.ResponseWriter, r *http.Request, goalID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	g, ok := s.loadGoal(r.Context(), w, goalID)
	if !ok {
		return
	}
	var req apitypes.CancelGoalRequest
	_ = decodeJSON(r, &req)
	if err := s.tasksSvc.Facade.CancelGoal(r.Context(), g.ID, strPtr(req.Reason), authActor(r)); err != nil {
		taskError(w, err)
		return
	}
	fresh, err := s.tasksSvc.Facade.GetGoal(r.Context(), g.ID)
	if err != nil {
		taskError(w, err)
		return
	}
	writeData(w, http.StatusOK, goalToAPI(fresh))
}

func (s *Server) ListGoalTasks(w http.ResponseWriter, r *http.Request, goalID string, params apiserver.ListGoalTasksParams) {
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
	g, ok := s.loadGoal(r.Context(), w, goalID)
	if !ok {
		return
	}
	rows, err := s.tasksSvc.Facade.ListGoalTasks(r.Context(), g.ID, int64(limit+1), int64(offset))
	if err != nil {
		taskError(w, err)
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

func (s *Server) ListGoalReviews(w http.ResponseWriter, r *http.Request, goalID string, params apiserver.ListGoalReviewsParams) {
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
	g, ok := s.loadGoal(r.Context(), w, goalID)
	if !ok {
		return
	}
	rows, err := s.tasksSvc.Facade.ListGoalReviews(r.Context(), g.ID, int64(limit+1), int64(offset))
	if err != nil {
		taskError(w, err)
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

func (s *Server) ApproveGoalReview(w http.ResponseWriter, r *http.Request, goalID string, reviewID string) {
	s.decideGoalReview(w, r, goalID, reviewID, "approve")
}

func (s *Server) RejectGoalReview(w http.ResponseWriter, r *http.Request, goalID string, reviewID string) {
	s.decideGoalReview(w, r, goalID, reviewID, "reject")
}

func (s *Server) RequestChangesGoalReview(w http.ResponseWriter, r *http.Request, goalID string, reviewID string) {
	s.decideGoalReview(w, r, goalID, reviewID, "request_changes")
}

func (s *Server) EscalateGoalReview(w http.ResponseWriter, r *http.Request, goalID string, reviewID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	g, ok := s.loadGoal(r.Context(), w, goalID)
	if !ok {
		return
	}
	rev, err := s.tasksSvc.Facade.GetReview(r.Context(), reviewID)
	if err != nil || !rev.GoalID.Valid || rev.GoalID.String != g.ID {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var req apitypes.EscalateReviewRequest
	_ = decodeJSON(r, &req)
	if err := s.tasksSvc.Facade.EscalateReview(r.Context(), reviewID, strPtr(req.Reason), authActor(r)); err != nil {
		taskError(w, err)
		return
	}
	fresh, err := s.tasksSvc.Facade.GetReview(r.Context(), reviewID)
	if err != nil {
		writeData(w, http.StatusOK, reviewToAPI(rev))
		return
	}
	writeData(w, http.StatusOK, reviewToAPI(fresh))
}

func (s *Server) decideGoalReview(w http.ResponseWriter, r *http.Request, goalID, reviewID, kind string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	if requireAuth(w, r) == nil {
		return
	}
	g, ok := s.loadGoal(r.Context(), w, goalID)
	if !ok {
		return
	}
	rev, err := s.tasksSvc.Facade.GetReview(r.Context(), reviewID)
	if err != nil || !rev.GoalID.Valid || rev.GoalID.String != g.ID {
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
		taskError(w, err)
		return
	}
	fresh, gerr := s.tasksSvc.Facade.GetReview(r.Context(), reviewID)
	if gerr != nil {
		writeData(w, http.StatusOK, reviewToAPI(rev))
		return
	}
	writeData(w, http.StatusOK, reviewToAPI(fresh))
}

// goalToAPI converts a sqlc.AgentGoal to its API shape.
func goalToAPI(g sqlc.AgentGoal) apitypes.Goal {
	out := apitypes.Goal{
		Id:           g.ID,
		UserId:       g.UserID,
		Title:        g.Title,
		Description:  optStr(g.Description),
		Status:       apitypes.GoalStatus(g.Status),
		Priority:     apitypes.GoalPriority(g.Priority),
		ReviewPolicy: apitypes.GoalReviewPolicy(g.ReviewPolicy),
		CreatedAt:    parseTS(g.CreatedAt),
		UpdatedAt:    parseTS(g.UpdatedAt),
	}
	if g.AgentID.Valid {
		v := g.AgentID.String
		out.AgentId = &v
	}
	if g.ActiveReviewID.Valid {
		v := g.ActiveReviewID.String
		out.ActiveReviewId = &v
	}
	if g.CompletedAt.Valid {
		v := parseTS(g.CompletedAt.String)
		out.CompletedAt = &v
	}
	if g.CancelledAt.Valid {
		v := parseTS(g.CancelledAt.String)
		out.CancelledAt = &v
	}
	if m := jsonObject(g.Context); m != nil {
		out.Context = m
	}
	if m := jsonObject(g.Output); m != nil {
		out.Output = m
	}
	return out
}
