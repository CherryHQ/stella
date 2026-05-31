package server

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func validateBaseDir(w http.ResponseWriter, agentID, userID, baseDir string) bool {
	userRoot, err := agent.SetupUserWorkspace(agentID, config.StellaHome(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve workspace")
		return false
	}
	if err := agent.ValidateProjectDir(baseDir, userRoot); err != nil {
		writeError(w, http.StatusBadRequest, "invalid base_dir: "+err.Error())
		return false
	}
	return true
}

func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request, agentID string, params apiserver.ListProjectsParams) {
	auth := UserFromContext(r.Context())
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	includeArchived := params.IncludeArchived != nil && *params.IncludeArchived

	var projects []sqlc.Project
	var err error
	if includeArchived {
		projects, err = s.q.ListProjectsAll(r.Context(), sqlc.ListProjectsAllParams{
			AgentID: agentID,
			UserID:  auth.UserID,
		})
	} else {
		projects, err = s.q.ListProjects(r.Context(), sqlc.ListProjectsParams{
			AgentID: agentID,
			UserID:  auth.UserID,
		})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := make([]projectResponse, 0, len(projects))
	for _, p := range projects {
		resp = append(resp, toProjectResponse(p))
	}
	writeData(w, http.StatusOK, map[string]any{"projects": resp})
}

func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request, agentID string) {
	auth := UserFromContext(r.Context())
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	var body apiserver.CreateProjectJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if body.BaseDir == "" {
		writeError(w, http.StatusBadRequest, "base_dir is required")
		return
	}

	if !validateBaseDir(w, agentID, auth.UserID, body.BaseDir) {
		return
	}

	p, err := s.q.CreateProject(r.Context(), sqlc.CreateProjectParams{
		ID:          uuid.NewString(),
		AgentID:     agentID,
		UserID:      auth.UserID,
		Name:        body.Name,
		BaseDir:     body.BaseDir,
		Description: sql.NullString{String: derefStr(body.Description), Valid: body.Description != nil},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusCreated, toProjectResponse(p))
}

func (s *Server) GetProject(w http.ResponseWriter, r *http.Request, agentID string, projectID string) {
	auth := UserFromContext(r.Context())
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	p, err := s.q.GetProject(r.Context(), sqlc.GetProjectParams{
		ID:     projectID,
		UserID: auth.UserID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p.AgentID != agentID {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	writeData(w, http.StatusOK, toProjectResponse(p))
}

func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request, agentID string, projectID string) {
	auth := UserFromContext(r.Context())
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	existing, err := s.q.GetProject(r.Context(), sqlc.GetProjectParams{
		ID:     projectID,
		UserID: auth.UserID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing.AgentID != agentID {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	var body apiserver.UpdateProjectJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	name := existing.Name
	if body.Name != nil {
		name = *body.Name
	}
	baseDir := existing.BaseDir
	if body.BaseDir != nil {
		if !validateBaseDir(w, agentID, auth.UserID, *body.BaseDir) {
			return
		}
		baseDir = *body.BaseDir
	}
	description := existing.Description
	if body.Description != nil {
		description = sql.NullString{String: *body.Description, Valid: true}
	}

	updated, err := s.q.UpdateProject(r.Context(), sqlc.UpdateProjectParams{
		Name:        name,
		Description: description,
		BaseDir:     baseDir,
		ID:          projectID,
		UserID:      auth.UserID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, toProjectResponse(updated))
}

func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request, agentID string, projectID string) {
	auth := UserFromContext(r.Context())
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	existing, err := s.q.GetProject(r.Context(), sqlc.GetProjectParams{
		ID:     projectID,
		UserID: auth.UserID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing.AgentID != agentID {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	if err := s.q.DeleteProject(r.Context(), sqlc.DeleteProjectParams{
		ID:     projectID,
		UserID: auth.UserID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type projectResponse struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	UserID      string    `json:"user_id"`
	Name        string    `json:"name"`
	BaseDir     string    `json:"base_dir"`
	Description string    `json:"description,omitempty"`
	Archived    bool      `json:"archived"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toProjectResponse(p sqlc.Project) projectResponse {
	r := projectResponse{
		ID:        p.ID,
		AgentID:   p.AgentID,
		UserID:    p.UserID,
		Name:      p.Name,
		BaseDir:   p.BaseDir,
		Archived:  p.Archived != 0,
		CreatedAt: parseTime(p.CreatedAt),
		UpdatedAt: parseTime(p.UpdatedAt),
	}
	if p.Description.Valid {
		r.Description = p.Description.String
	}
	return r
}
