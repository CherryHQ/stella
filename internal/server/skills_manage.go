package server

import (
	"errors"
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/skillaccess"
	"github.com/CherryHQ/stella/internal/skills"
)

// writeConflictOrInternal maps a duplicate-name store error to 409 and any other
// error to 500.
func (s *Server) writeConflictOrInternal(w http.ResponseWriter, err error) {
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "a skill with this name already exists in this scope")
		return
	}
	s.writeInternalError(w, err)
}

// Scoped skill management API (/api/skills*) powers the dedicated Settings →
// Skills page. Unlike the agent-scoped endpoints in skills_scoped.go, these
// operate across agents and key on scope rather than an agent path, so they
// can reach user-global and system-global skills that aren't tied to an agent.
//
// The four scopes mirror scoped credential envs:
//   - user          managed by the owning user, available across all their agents
//   - user_agent    managed by the owning user, scoped to one agent
//   - system        managed by admins, available everywhere
//   - system_agent  managed by admins, scoped to one agent

// skillScopeOwner maps a (scope, userID, agentID) triple to the owner columns a
// skill row of that scope is allowed to carry, per the skill table CHECK.
func skillScopeOwner(scope, userID, agentID string) (uid, aid string) {
	switch scope {
	case "user":
		return userID, ""
	case "user_agent":
		return userID, agentID
	case "system_agent":
		return "", agentID
	default: // system
		return "", ""
	}
}

// resolveSkillManageScope validates the requested management scope shape and
// authorizes managing that scope bucket, returning the owner columns a row of
// that scope carries. user/user_agent bind the acting user; system/system_agent
// require the admin superuser; agent-bound scopes fold an agent-read gate into
// the authorization. On failure it writes the response and returns ok=false.
func (s *Server) resolveSkillManageScope(w http.ResponseWriter, r *http.Request, info *AuthInfo, scope, agentID string) (string, string, *skillaccess.Access, bool) {
	switch scope {
	case "user", "system":
		agentID = ""
	case "user_agent":
		if agentID == "" {
			writeError(w, http.StatusBadRequest, "agent_id is required for user_agent scope")
			return "", "", nil, false
		}
	case "system_agent":
		if agentID == "" {
			writeError(w, http.StatusBadRequest, "agent_id is required for system_agent scope")
			return "", "", nil, false
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid scope")
		return "", "", nil, false
	}
	acc, code, msg := s.beginSkillAccess(r.Context())
	if code != 0 {
		writeError(w, code, msg)
		return "", "", nil, false
	}
	uid, aid, err := acc.AuthorizeManageScope(r.Context(), scope, agentID)
	if err != nil {
		code, msg := skillAccessError(err)
		writeError(w, code, msg)
		return "", "", nil, false
	}
	return uid, aid, acc, true
}

// scopedSkillByID loads a DB skill by id and authorizes the given action. It
// writes the error response and returns nil when the caller may not perform the
// action (opaque 404 for a foreign user skill, 403 for an admin-managed system
// skill).
func (s *Server) scopedSkillByID(w http.ResponseWriter, r *http.Request, id string, action authz.Action) *skills.Skill {
	acc, code, msg := s.beginSkillAccess(r.Context())
	if code != 0 {
		writeError(w, code, msg)
		return nil
	}
	sk, err := acc.AuthorizeManageByID(r.Context(), id, action)
	if err != nil {
		code, msg := skillAccessError(err)
		writeError(w, code, msg)
		return nil
	}
	return &sk
}

func (s *Server) dbSkillView(r *http.Request, sk *skills.Skill) skillView {
	files, _ := s.skillStore().ListFiles(r.Context(), sk.ID)
	view := storedSkillToView(*sk, files)
	return view
}

// ListScopedSkills handles GET /api/skills.
func (s *Server) ListScopedSkills(w http.ResponseWriter, r *http.Request, params apiserver.ListScopedSkillsParams) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	scope := "user"
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	agentID := ""
	if params.AgentId != nil {
		agentID = *params.AgentId
	}
	userID, agentID, acc, ok := s.resolveSkillManageScope(w, r, info, scope, agentID)
	if !ok {
		return
	}
	rows, err := s.skillStore().ListByScope(r.Context(), scope, userID, agentID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	out := make([]skillView, 0, len(rows))
	for i := range rows {
		if err := acc.AuthorizeRead(r.Context(), rows[i]); err != nil {
			if errors.Is(err, skillaccess.ErrNotFound) || errors.Is(err, skillaccess.ErrForbidden) {
				continue
			}
			s.writeInternalError(w, err)
			return
		}
		out = append(out, s.dbSkillView(r, &rows[i]))
	}
	writeData(w, http.StatusOK, map[string]any{"skills": out})
}

// CreateScopedSkill handles POST /api/skills.
func (s *Server) CreateScopedSkill(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req struct {
		Name                   string            `json:"name"`
		Scope                  string            `json:"scope"`
		AgentID                string            `json:"agent_id"`
		Description            string            `json:"description"`
		Status                 string            `json:"status"`
		DisableModelInvocation bool              `json:"disable_model_invocation"`
		Files                  map[string]string `json:"files"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	userID, agentID, _, ok := s.resolveSkillManageScope(w, r, info, req.Scope, req.AgentID)
	if !ok {
		return
	}
	files := req.Files
	if files == nil {
		files = map[string]string{skills.MainFile: "---\nname: " + req.Name + "\ndescription: " + req.Description + "\n---\n"}
	}
	if files[skills.MainFile] == "" {
		writeError(w, http.StatusBadRequest, "files must include SKILL.md")
		return
	}
	uid, aid := skillScopeOwner(req.Scope, userID, agentID)
	sk := skills.Skill{
		Scope:                  req.Scope,
		UserID:                 uid,
		AgentID:                aid,
		Name:                   req.Name,
		Description:            req.Description,
		Status:                 req.Status,
		DisableModelInvocation: req.DisableModelInvocation,
	}
	id, err := s.skillStore().Create(r.Context(), sk, files)
	if err != nil {
		s.writeConflictOrInternal(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"id": id, "name": req.Name})
}

// InstallScopedSkill handles POST /api/skills/install.
func (s *Server) InstallScopedSkill(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req struct {
		Source  string `json:"source"`
		Scope   string `json:"scope"`
		AgentID string `json:"agent_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "source is required")
		return
	}
	userID, agentID, _, ok := s.resolveSkillManageScope(w, r, info, req.Scope, req.AgentID)
	if !ok {
		return
	}
	ctx := r.Context()
	if skills.GitHubSource(req.Source) {
		// Use the acting user's bound token, not the store owner — system-scope
		// installs resolve userID to "" yet are still performed by a real admin.
		if token := s.credSvc.GitHubAccessToken(ctx, info.UserID); token != "" {
			ctx = skills.WithGitHubToken(ctx, token)
		}
	}
	name, err := skills.InstallToStore(ctx, pluginhost.NewSkillStoreAdapter(s.skillStore()), req.Source, req.Scope, userID, agentID)
	if err != nil {
		s.writeConflictOrInternal(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"name": name})
}

// UploadScopedSkill handles POST /api/skills/upload.
func (s *Server) UploadScopedSkill(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	up, code, msg, err := parseUploadedSkill(r)
	if code != 0 {
		if err != nil {
			s.log.Warn("skill upload rejected", "status", code, "message", msg, "error", err)
		}
		writeError(w, code, msg)
		return
	}
	scope := r.FormValue("scope")
	userID, agentID, _, ok := s.resolveSkillManageScope(w, r, info, scope, r.FormValue("agent_id"))
	if !ok {
		return
	}
	uid, aid := skillScopeOwner(scope, userID, agentID)
	sk := skills.Skill{
		Scope:                  scope,
		UserID:                 uid,
		AgentID:                aid,
		Name:                   up.name,
		Description:            up.description,
		Status:                 up.status,
		DisableModelInvocation: up.disableModelInvocation,
		Metadata:               up.metadata,
	}
	id, err := s.skillStore().Create(r.Context(), sk, up.files)
	if err != nil {
		s.writeConflictOrInternal(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"id": id, "name": up.name})
}

// GetScopedSkill handles GET /api/skills/{id}.
func (s *Server) GetScopedSkill(w http.ResponseWriter, r *http.Request, id string) {
	sk := s.scopedSkillByID(w, r, id, authz.ActionRead)
	if sk == nil {
		return
	}
	writeData(w, http.StatusOK, s.dbSkillView(r, sk))
}

// UpdateScopedSkill handles PATCH /api/skills/{id}.
func (s *Server) UpdateScopedSkill(w http.ResponseWriter, r *http.Request, id string) {
	sk := s.scopedSkillByID(w, r, id, authz.ActionWrite)
	if sk == nil {
		return
	}
	s.applySkillUpdate(w, r, sk)
}

// DeleteScopedSkill handles DELETE /api/skills/{id}.
func (s *Server) DeleteScopedSkill(w http.ResponseWriter, r *http.Request, id string) {
	sk := s.scopedSkillByID(w, r, id, authz.ActionDelete)
	if sk == nil {
		return
	}
	s.doDeleteSkill(w, r, sk.ID)
}

// GetScopedSkillFile handles GET /api/skills/{id}/file.
func (s *Server) GetScopedSkillFile(w http.ResponseWriter, r *http.Request, id string, params apiserver.GetScopedSkillFileParams) {
	sk := s.scopedSkillByID(w, r, id, authz.ActionRead)
	if sk == nil {
		return
	}
	content, err := s.skillStore().LoadFile(r.Context(), sk.ID, params.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeData(w, http.StatusOK, map[string]string{"path": params.Path, "content": content})
}

// DeleteScopedSkillFile handles DELETE /api/skills/{id}/file.
func (s *Server) DeleteScopedSkillFile(w http.ResponseWriter, r *http.Request, id string, params apiserver.DeleteScopedSkillFileParams) {
	sk := s.scopedSkillByID(w, r, id, authz.ActionWrite)
	if sk == nil {
		return
	}
	s.doDeleteSkillFile(w, r, sk.ID, params.Path)
}
