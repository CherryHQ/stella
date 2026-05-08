package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/skills"
	skillstool "github.com/CherryHQ/stella/plugins/tools/skills"
	mcpskills "github.com/vaayne/mcphub/pkg/skills"
)

// skillView is the JSON representation of a skill returned to the admin UI.
type skillView struct {
	ID                     string   `json:"id"`
	Scope                  string   `json:"scope"`
	UserID                 int64    `json:"user_id,omitempty"`
	AgentID                string   `json:"agent_id,omitempty"`
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

func (s *Server) ListSkills(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
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

func (s *Server) GetSkill(w http.ResponseWriter, r *http.Request, id string) {
	if !requireAdmin(w, r) {
		return
	}
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
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

func (s *Server) GetSkillFile(w http.ResponseWriter, r *http.Request, id string, params apiserver.GetSkillFileParams) {
	if !requireAdmin(w, r) {
		return
	}
	s.serveSkillFile(w, r, id, params.Path)
}

// serveSkillFile is the shared body of GET .../skills/{id}/file?path=...
// (reused by scoped handlers after their ownership checks).
func (s *Server) serveSkillFile(w http.ResponseWriter, r *http.Request, id, path string) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
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

// DeleteSkillFile removes a single file under a skill (admin-only route).
func (s *Server) DeleteSkillFile(w http.ResponseWriter, r *http.Request, id string, params apiserver.DeleteSkillFileParams) {
	if !requireAdmin(w, r) {
		return
	}
	s.doDeleteSkillFile(w, r, id, params.Path)
}

// doDeleteSkillFile is the shared body of DELETE .../skills/{id}/file?path=...
func (s *Server) doDeleteSkillFile(w http.ResponseWriter, r *http.Request, id, path string) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}
	if path == skills.MainFile {
		writeError(w, http.StatusBadRequest, "cannot delete SKILL.md")
		return
	}
	if err := store.DeleteFile(r.Context(), id, path); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"path": path})
}

type createSkillRequest struct {
	Scope                  string            `json:"scope"`
	UserID                 int64             `json:"user_id"`
	AgentID                string            `json:"agent_id"`
	Name                   string            `json:"name"`
	Description            string            `json:"description"`
	Status                 string            `json:"status"`
	DisableModelInvocation bool              `json:"disable_model_invocation"`
	Files                  map[string]string `json:"files"`
}

func (s *Server) CreateSkill(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
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
	case "system":
		// no owner field required
	default:
		writeError(w, http.StatusBadRequest, "scope must be one of: system, agent, user")
		return
	}

	sk := skills.Skill{
		Scope:                  req.Scope,
		UserID:                 req.UserID,
		AgentID:                req.AgentID,
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

func (s *Server) UpdateSkill(w http.ResponseWriter, r *http.Request, id string) {
	if !requireAdmin(w, r) {
		return
	}
	s.applySkillUpdate(w, r, id)
}

// applySkillUpdate is the shared body for PUT .../skills/{id}.
func (s *Server) applySkillUpdate(w http.ResponseWriter, r *http.Request, id string) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
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

func (s *Server) DeleteSkill(w http.ResponseWriter, r *http.Request, id string) {
	if !requireAdmin(w, r) {
		return
	}
	s.doDeleteSkill(w, r, id)
}

// doDeleteSkill is the shared body for DELETE .../skills/{id}.
func (s *Server) doDeleteSkill(w http.ResponseWriter, r *http.Request, id string) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	if err := store.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"id": id})
}

// SearchSkills handles GET /api/skills/search?q=<query>&limit=<n>.
// It queries mcphub for skills matching the query. Errors from the upstream
// search API are returned as 502 (bad gateway) since they are not our fault.
func (s *Server) SearchSkills(w http.ResponseWriter, r *http.Request, params apiserver.SearchSkillsParams) {
	q := params.Q
	limit := 10
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
	}
	if limit > 50 {
		limit = 50
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	results, err := mcpskills.Search(ctx, q, limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeData(w, http.StatusOK, results)
}

// installSkillRequest is the body for POST /api/skills/install.
type installSkillRequest struct {
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	UserID  int64  `json:"user_id"`
	AgentID string `json:"agent_id"`
}

// InstallSkill handles POST /api/skills/install.
// It delegates to skillstool.InstallToStore to fetch and store the skill.
// "Actually install from a real GitHub repo" is integration-level and should be
// tested manually — unit tests cover only validation and auth.
func (s *Server) InstallSkill(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	var req installSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "source is required")
		return
	}
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
	case "system":
		// no owner field required
	default:
		writeError(w, http.StatusBadRequest, "scope must be one of: system, agent, user")
		return
	}

	name, err := skillstool.InstallToStore(r.Context(), pluginhost.NewSkillStoreAdapter(store), req.Source, req.Scope, req.UserID, req.AgentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"name": name})
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
		Name:                   sk.Name,
		Description:            sk.Description,
		Status:                 sk.Status,
		DisableModelInvocation: sk.DisableModelInvocation,
		Files:                  files,
		CreatedAt:              sk.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:              sk.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}
