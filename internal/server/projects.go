package server

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request, agentID string, params apiserver.ListProjectsParams) {
	auth := UserFromContext(r.Context())
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
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
	writeData(w, http.StatusOK, resp)
}

func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request, agentID string) {
	auth := UserFromContext(r.Context())
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
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

	writeData(w, http.StatusOK, toProjectResponse(p))
}

func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request, agentID string, projectID string) {
	auth := UserFromContext(r.Context())
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
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

	_, err := s.q.GetProject(r.Context(), sqlc.GetProjectParams{
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
	ID          string `json:"id"`
	AgentID     string `json:"agent_id"`
	UserID      string `json:"user_id"`
	Name        string `json:"name"`
	BaseDir     string `json:"base_dir"`
	Description string `json:"description,omitempty"`
	Archived    bool   `json:"archived"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toProjectResponse(p sqlc.Project) projectResponse {
	r := projectResponse{
		ID:        p.ID,
		AgentID:   p.AgentID,
		UserID:    p.UserID,
		Name:      p.Name,
		BaseDir:   p.BaseDir,
		Archived:  p.Archived != 0,
		CreatedAt: parseProjectTime(p.CreatedAt),
		UpdatedAt: parseProjectTime(p.UpdatedAt),
	}
	if p.Description.Valid {
		r.Description = p.Description.String
	}
	return r
}

func parseProjectTime(s string) string {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return s
	}
	return t.UTC().Format(time.RFC3339)
}
