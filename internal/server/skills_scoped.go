package server

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"path"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/skills"
	skillstool "github.com/CherryHQ/stella/internal/tools/skills"
	"github.com/CherryHQ/stella/resources"
)

// findSkillByID linear-scans ListAll. The store has no Get(ctx, id) yet —
// see handoff.md "Blockers/Gotchas". Fine at current volumes.
func (s *Server) findSkillByID(ctx context.Context, id string) (*skills.Skill, error) {
	rows, err := s.skillStore().ListAll(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func (s *Server) requireAgentAccess(ctx context.Context, agentID string) (config.Agent, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return config.Agent{}, http.StatusUnauthorized, "unauthorized"
	}
	a, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return config.Agent{}, http.StatusNotFound, "agent not found"
		}
		return config.Agent{}, http.StatusInternalServerError, err.Error()
	}
	if !info.IsAdmin && !s.canAccessAgent(ctx, info, a) {
		return config.Agent{}, http.StatusForbidden, "forbidden"
	}
	return a, 0, ""
}

// requireAgentManage verifies the caller is an admin or the agent creator.
// Returns (agent, 0, "") on success, (_, status, msg) on failure.
func (s *Server) requireAgentManage(ctx context.Context, agentID string) (config.Agent, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return config.Agent{}, http.StatusUnauthorized, "unauthorized"
	}
	a, code, msg := s.requireAgentAccess(ctx, agentID)
	if code != 0 {
		return config.Agent{}, code, msg
	}
	if !info.IsAdmin && a.CreatorID != info.UserID {
		return config.Agent{}, http.StatusForbidden, "forbidden"
	}
	return a, 0, ""
}

// requireSkillScope checks that skill matches the expected scope and owner.
// Returns the skill or an error status + message.
func (s *Server) requireSkillScope(ctx context.Context, id, scope string, userID string, agentID string) (*skills.Skill, int, string) {
	sk, err := s.findSkillByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, http.StatusNotFound, "skill not found"
		}
		return nil, http.StatusInternalServerError, err.Error()
	}
	if sk.Scope != scope {
		return nil, http.StatusNotFound, "skill not found"
	}
	switch scope {
	case "agent":
		if sk.AgentID != agentID {
			return nil, http.StatusNotFound, "skill not found"
		}
	case "user":
		if sk.UserID != userID || (agentID != "" && sk.AgentID != agentID) {
			return nil, http.StatusNotFound, "skill not found"
		}
	case "system":
		// no owner check
	}
	return sk, 0, ""
}

func builtinSkillFiles(id string) (map[string]string, error) {
	sub, ok := resources.SubFS(resources.KindSkill)
	if !ok {
		return nil, fs.ErrNotExist
	}
	var root string
	err := fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || p == "." {
			return nil
		}
		if path.Base(p) != id {
			return nil
		}
		if _, err := fs.Stat(sub, path.Join(p, skills.MainFile)); err == nil {
			root = p
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, err
	}
	if root == "" {
		return nil, fs.ErrNotExist
	}
	files := map[string]string{}
	if err := fs.WalkDir(sub, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		files[path.Clean(p[len(root)+1:])] = string(data)
		return nil
	}); err != nil {
		return nil, err
	}
	return files, nil
}

// ---- Agent-scoped skills: /api/agents/{id}/skills* ----

func (s *Server) ListAgentSkills(w http.ResponseWriter, r *http.Request, id string) {
	agentID := id
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	all, err := s.skillStore().ListAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]skillView, 0)
	for _, sk := range all {
		if sk.Status == "deprecated" {
			continue
		}
		switch sk.Scope {
		case "system":
			out = append(out, skillToView(sk, nil))
		case "agent":
			if sk.AgentID == agentID {
				out = append(out, skillToView(sk, nil))
			}
		case "user":
			if sk.UserID == info.UserID && sk.AgentID == agentID {
				out = append(out, skillToView(sk, nil))
			}
		}
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) requireAgentSkillWrite(ctx context.Context, agentID, scope string) (string, skills.ViewContext, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return "", skills.ViewContext{}, http.StatusUnauthorized, "unauthorized"
	}
	switch scope {
	case "agent":
		if _, code, msg := s.requireAgentManage(ctx, agentID); code != 0 {
			return "", skills.ViewContext{}, code, msg
		}
		return info.UserID, skills.ViewContext{AgentID: agentID}, 0, ""
	case "user":
		if _, code, msg := s.requireAgentAccess(ctx, agentID); code != 0 {
			return "", skills.ViewContext{}, code, msg
		}
		return info.UserID, skills.ViewContext{UserID: info.UserID, AgentID: agentID}, 0, ""
	case "system":
		return "", skills.ViewContext{}, http.StatusForbidden, "system skills are read-only"
	default:
		return "", skills.ViewContext{}, http.StatusBadRequest, "scope must be one of: system, agent, user"
	}
}

func (s *Server) requireAgentSkillRead(ctx context.Context, agentID, scope, skillID string) (*skills.Skill, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return nil, http.StatusUnauthorized, "unauthorized"
	}
	if _, code, msg := s.requireAgentAccess(ctx, agentID); code != 0 {
		return nil, code, msg
	}
	userID := ""
	if scope == "user" {
		userID = info.UserID
	}
	return s.requireSkillScope(ctx, skillID, scope, userID, agentID)
}

func (s *Server) CreateAgentScopedSkill(w http.ResponseWriter, r *http.Request, id string, scope string) {
	agentID := id
	userID, _, code, msg := s.requireAgentSkillWrite(r.Context(), agentID, scope)
	if code != 0 {
		writeError(w, code, msg)
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
	files := req.Files
	if files == nil {
		files = map[string]string{skills.MainFile: "---\nname: " + req.Name + "\ndescription: " + req.Description + "\n---\n"}
	}
	if files[skills.MainFile] == "" {
		writeError(w, http.StatusBadRequest, "files must include SKILL.md")
		return
	}
	sk := skills.Skill{
		Scope:                  scope,
		AgentID:                agentID,
		Name:                   req.Name,
		Description:            req.Description,
		Status:                 req.Status,
		DisableModelInvocation: req.DisableModelInvocation,
	}
	if scope == "user" {
		sk.UserID = userID
	}
	createdID, err := s.skillStore().Create(r.Context(), sk, files)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"id": createdID, "name": req.Name})
}

func (s *Server) GetAgentScopedSkill(w http.ResponseWriter, r *http.Request, id string, scope string, skillId string) {
	sk, code, msg := s.requireAgentSkillRead(r.Context(), id, scope, skillId)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	paths, err := s.skillStore().ListFiles(r.Context(), sk.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, skillToView(*sk, paths))
}

func (s *Server) GetAgentScopedSkillFile(w http.ResponseWriter, r *http.Request, id string, scope string, skillId string, params apiserver.GetAgentScopedSkillFileParams) {
	if _, code, msg := s.requireAgentSkillRead(r.Context(), id, scope, skillId); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.serveSkillFile(w, r, skillId, params.Path)
}

func (s *Server) UpdateAgentScopedSkill(w http.ResponseWriter, r *http.Request, id string, scope string, skillId string) {
	_, vc, code, msg := s.requireAgentSkillWrite(r.Context(), id, scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), skillId, scope, vc.UserID, id); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.applySkillUpdate(w, r, skillId, vc)
}

func (s *Server) DeleteAgentScopedSkill(w http.ResponseWriter, r *http.Request, id string, scope string, skillId string) {
	_, vc, code, msg := s.requireAgentSkillWrite(r.Context(), id, scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), skillId, scope, vc.UserID, id); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.doDeleteSkill(w, r, skillId, vc)
}

func (s *Server) DeleteAgentScopedSkillFile(w http.ResponseWriter, r *http.Request, id string, scope string, skillId string, params apiserver.DeleteAgentScopedSkillFileParams) {
	_, vc, code, msg := s.requireAgentSkillWrite(r.Context(), id, scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), skillId, scope, vc.UserID, id); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.doDeleteSkillFile(w, r, skillId, params.Path)
}

func (s *Server) InstallAgentScopedSkill(w http.ResponseWriter, r *http.Request, id string, scope string) {
	userID, _, code, msg := s.requireAgentSkillWrite(r.Context(), id, scope)
	if code != 0 {
		writeError(w, code, msg)
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
	storeUserID := ""
	if scope == "user" {
		storeUserID = userID
	}
	name, err := skillstool.InstallToStore(r.Context(), pluginhost.NewSkillStoreAdapter(s.skillStore()), req.Source, scope, storeUserID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"name": name})
}

func (s *Server) GetAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string) {
	agentID := id
	skillID := skillId
	if _, code, msg := s.requireAgentManage(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	sk, code, msg := s.requireSkillScope(r.Context(), skillID, "agent", "", agentID)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	paths, err := s.skillStore().ListFiles(r.Context(), sk.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, skillToView(*sk, paths))
}

func (s *Server) UpdateAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string) {
	agentID := id
	skillID := skillId
	if _, code, msg := s.requireAgentManage(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), skillID, "agent", "", agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.applySkillUpdate(w, r, skillID, skills.ViewContext{AgentID: agentID})
}

func (s *Server) DeleteAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string) {
	agentID := id
	skillID := skillId
	if _, code, msg := s.requireAgentManage(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), skillID, "agent", "", agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.doDeleteSkill(w, r, skillID, skills.ViewContext{AgentID: agentID})
}

func (s *Server) InstallAgentSkill(w http.ResponseWriter, r *http.Request, id string) {
	agentID := id
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !info.IsAdmin {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if _, err := s.store.GetAgent(r.Context(), agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
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
	name, err := skillstool.InstallToStore(r.Context(), pluginhost.NewSkillStoreAdapter(s.skillStore()), req.Source, "agent", "", agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"name": name})
}

func (s *Server) DuplicateBuiltinSkillToAgent(w http.ResponseWriter, r *http.Request, id string, skillId string) {
	agentID := id
	skillID := skillId
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !info.IsAdmin {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if _, err := s.store.GetAgent(r.Context(), agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	reg, err := resources.Default()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	res, ok := reg.Get(resources.KindSkill, skillID)
	if !ok {
		writeError(w, http.StatusNotFound, "builtin skill not found")
		return
	}
	files, err := builtinSkillFiles(skillID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "builtin skill not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	createdID, err := s.skillStore().Create(r.Context(), skills.Skill{
		Scope:       "agent",
		AgentID:     agentID,
		Name:        res.Name,
		Description: res.Description,
		Status:      "active",
		Metadata:    []byte(`{"source":"builtin"}`),
	}, files)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"id": createdID, "name": res.Name})
}

// ---- Profile (self-user) skills: /api/auth/profile/skills* ----

func (s *Server) ListProfileSkills(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	all, err := s.skillStore().ListAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]skillView, 0)
	for _, sk := range all {
		if sk.Scope == "user" && sk.UserID == info.UserID {
			out = append(out, skillToView(sk, nil))
		}
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) GetProfileSkill(w http.ResponseWriter, r *http.Request, skillId string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	skillID := skillId
	sk, code, msg := s.requireSkillScope(r.Context(), skillID, "user", info.UserID, "")
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	paths, err := s.skillStore().ListFiles(r.Context(), sk.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, skillToView(*sk, paths))
}

func (s *Server) UpdateProfileSkill(w http.ResponseWriter, r *http.Request, skillId string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	skillID := skillId
	if _, code, msg := s.requireSkillScope(r.Context(), skillID, "user", info.UserID, ""); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.applySkillUpdate(w, r, skillID, skills.ViewContext{UserID: info.UserID})
}

func (s *Server) DeleteProfileSkill(w http.ResponseWriter, r *http.Request, skillId string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	skillID := skillId
	if _, code, msg := s.requireSkillScope(r.Context(), skillID, "user", info.UserID, ""); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.doDeleteSkill(w, r, skillID, skills.ViewContext{UserID: info.UserID})
}

func (s *Server) InstallProfileSkill(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
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
	name, err := skillstool.InstallToStore(r.Context(), pluginhost.NewSkillStoreAdapter(s.skillStore()), req.Source, "user", info.UserID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"name": name})
}
