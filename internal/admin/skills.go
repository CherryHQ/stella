package admin

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/vaayne/anna/internal/skills"
)

// skillView is the JSON representation of a skill returned to the admin UI.
type skillView struct {
	ID                     string   `json:"id"`
	Scope                  string   `json:"scope"`
	UserID                 int64    `json:"user_id,omitempty"`
	AgentID                string   `json:"agent_id,omitempty"`
	Project                string   `json:"project,omitempty"`
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	Status                 string   `json:"status"`
	DisableModelInvocation bool     `json:"disable_model_invocation"`
	Files                  []string `json:"files"`
	CreatedAt              string   `json:"created_at"`
	UpdatedAt              string   `json:"updated_at"`
}

func (s *Server) skillStore() skills.Store {
	return s.pluginHost.SkillStore()
}

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	all, err := store.ListAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]skillView, 0, len(all))
	for _, sk := range all {
		out = append(out, skillToView(sk, nil))
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) getSkill(w http.ResponseWriter, r *http.Request) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	id := r.PathValue("id")
	row, err := store.ListAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var found *skills.Skill
	for i := range row {
		if row[i].ID == id {
			found = &row[i]
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}
	paths, err := store.ListFiles(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, skillToView(*found, paths))
}

func (s *Server) getSkillFile(w http.ResponseWriter, r *http.Request) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	id := r.PathValue("id")
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}
	content, err := store.LoadFile(r.Context(), id, path)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"path": path, "content": content})
}

type createSkillRequest struct {
	Scope                  string            `json:"scope"`
	UserID                 int64             `json:"user_id"`
	AgentID                string            `json:"agent_id"`
	Project                string            `json:"project"`
	Name                   string            `json:"name"`
	Description            string            `json:"description"`
	Status                 string            `json:"status"`
	DisableModelInvocation bool              `json:"disable_model_invocation"`
	Files                  map[string]string `json:"files"`
}

func (s *Server) createSkill(w http.ResponseWriter, r *http.Request) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	var req createSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Scope == "" {
		writeError(w, http.StatusBadRequest, "scope is required")
		return
	}
	if req.Files == nil || req.Files[skills.MainFile] == "" {
		writeError(w, http.StatusBadRequest, "files must include SKILL.md")
		return
	}
	// Validate scope-specific owner fields.
	switch req.Scope {
	case "user":
		if req.UserID == 0 {
			writeError(w, http.StatusBadRequest, "user_id is required for scope=user")
			return
		}
	case "agent":
		if req.AgentID == "" {
			writeError(w, http.StatusBadRequest, "agent_id is required for scope=agent")
			return
		}
	case "project":
		if req.Project == "" {
			writeError(w, http.StatusBadRequest, "project is required for scope=project")
			return
		}
	case "system":
		// no owner field required
	default:
		writeError(w, http.StatusBadRequest, "scope must be one of: system, agent, user, project")
		return
	}

	sk := skills.Skill{
		Scope:                  req.Scope,
		UserID:                 req.UserID,
		AgentID:                req.AgentID,
		Project:                req.Project,
		Name:                   req.Name,
		Description:            req.Description,
		Status:                 req.Status,
		DisableModelInvocation: req.DisableModelInvocation,
	}
	id, err := store.Create(r.Context(), sk, req.Files)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"id": id})
}

type updateSkillRequest struct {
	Description            *string           `json:"description"`
	Status                 *string           `json:"status"`
	DisableModelInvocation *bool             `json:"disable_model_invocation"`
	Files                  map[string]string `json:"files"`
}

func (s *Server) updateSkill(w http.ResponseWriter, r *http.Request) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	id := r.PathValue("id")
	var req updateSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	patch := skills.UpdatePatch{
		Description:            req.Description,
		Status:                 req.Status,
		DisableModelInvocation: req.DisableModelInvocation,
	}
	if err := store.Update(r.Context(), id, patch); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for path, content := range req.Files {
		if err := store.UpsertFile(r.Context(), id, path, content); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeData(w, http.StatusOK, map[string]string{"id": id})
}

func (s *Server) deleteSkill(w http.ResponseWriter, r *http.Request) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	id := r.PathValue("id")
	if err := store.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"id": id})
}

func skillToView(sk skills.Skill, files []string) skillView {
	if files == nil {
		files = []string{}
	}
	return skillView{
		ID:                     sk.ID,
		Scope:                  sk.Scope,
		UserID:                 sk.UserID,
		AgentID:                sk.AgentID,
		Project:                sk.Project,
		Name:                   sk.Name,
		Description:            sk.Description,
		Status:                 sk.Status,
		DisableModelInvocation: sk.DisableModelInvocation,
		Files:                  files,
		CreatedAt:              sk.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:              sk.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}
