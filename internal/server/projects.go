package server

import (
	"errors"
	"net/http"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
)

// projectError maps a project use-case error to its HTTP status and message,
// preserving the historical bodies. Agent-gate denials reuse the agent access
// mapping; anything unrecognized is a logged 500.
func projectError(err error) (int, string) {
	switch {
	case err == nil:
		return 0, ""
	case errors.Is(err, agent.ErrProjectNotFound):
		return http.StatusNotFound, "project not found"
	case errors.Is(err, agent.ErrInvalidBaseDir):
		return http.StatusBadRequest, "invalid base_dir"
	case errors.Is(err, agentaccess.ErrNotFound), errors.Is(err, agentaccess.ErrForbidden):
		return agentAccessError(err)
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// writeProjectError writes the mapped project error and logs unrecognized 500s.
func (s *Server) writeProjectError(w http.ResponseWriter, err error) {
	code, msg := projectError(err)
	switch code {
	case http.StatusInternalServerError:
		s.writeInternalError(w, err)
	default:
		writeError(w, code, msg)
	}
}

func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request, agentID string, params apiserver.ListProjectsParams) {
	authority, ok := s.projectAuthority(w, r)
	if !ok {
		return
	}
	includeArchived := params.IncludeArchived != nil && *params.IncludeArchived
	projects, err := s.projectStore.List(r.Context(), authority, agentID, includeArchived)
	if err != nil {
		s.writeProjectError(w, err)
		return
	}
	resp := make([]projectResponse, 0, len(projects))
	for _, p := range projects {
		resp = append(resp, toProjectResponse(p))
	}
	writeData(w, http.StatusOK, map[string]any{"projects": resp})
}

func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request, agentID string) {
	authority, ok := s.projectAuthority(w, r)
	if !ok {
		return
	}
	var body apiserver.CreateProjectJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
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
	p, err := s.projectStore.Create(r.Context(), authority, agentID, body.Name, body.BaseDir, body.Description)
	if err != nil {
		s.writeProjectError(w, err)
		return
	}
	writeData(w, http.StatusCreated, toProjectResponse(p))
}

func (s *Server) GetProject(w http.ResponseWriter, r *http.Request, agentID string, projectID string) {
	authority, ok := s.projectAuthority(w, r)
	if !ok {
		return
	}
	p, err := s.projectStore.Get(r.Context(), authority, agentID, projectID)
	if err != nil {
		s.writeProjectError(w, err)
		return
	}
	writeData(w, http.StatusOK, toProjectResponse(p))
}

func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request, agentID string, projectID string) {
	authority, ok := s.projectAuthority(w, r)
	if !ok {
		return
	}
	var body apiserver.UpdateProjectJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	updated, err := s.projectStore.Update(r.Context(), authority, agentID, projectID, agent.ProjectUpdate{
		Name:        body.Name,
		BaseDir:     body.BaseDir,
		Description: body.Description,
	})
	if err != nil {
		s.writeProjectError(w, err)
		return
	}
	writeData(w, http.StatusOK, toProjectResponse(updated))
}

func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request, agentID string, projectID string) {
	authority, ok := s.projectAuthority(w, r)
	if !ok {
		return
	}
	if err := s.projectStore.Delete(r.Context(), authority, agentID, projectID); err != nil {
		s.writeProjectError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// projectAuthority resolves the trusted Authority for a project request, writing
// the 401/403 response when the caller is not authenticated.
func (s *Server) projectAuthority(w http.ResponseWriter, r *http.Request) (authz.Authority, bool) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return authz.Authority{}, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return authz.Authority{}, false
	}
	return authority, true
}

type projectResponse struct {
	ID            string    `json:"id"`
	AgentID       string    `json:"agent_id"`
	UserID        string    `json:"user_id"`
	Name          string    `json:"name"`
	BaseDir       string    `json:"base_dir"`
	Description   string    `json:"description,omitempty"`
	Archived      bool      `json:"archived"`
	IsUnavailable bool      `json:"is_unavailable"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func toProjectResponse(p agent.Project) projectResponse {
	return projectResponse{
		ID:            p.ID,
		AgentID:       p.AgentID,
		UserID:        p.UserID,
		Name:          p.Name,
		BaseDir:       p.BaseDir,
		Description:   p.Description,
		Archived:      p.Archived,
		IsUnavailable: p.IsUnavailable,
		CreatedAt:     p.CreatedAt,
		UpdatedAt:     p.UpdatedAt,
	}
}
