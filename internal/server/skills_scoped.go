package server

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/skills"
	skillstool "github.com/CherryHQ/stella/internal/tools/skills"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func (s *Server) skillService() *skillstool.Service {
	return skillstool.NewService(pluginhost.NewSkillStoreAdapter(s.skillStore()), config.StellaHome())
}

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

// ---- helpers ----------------------------------------------------------------

func (s *Server) projectRootForSession(ctx context.Context, agentID string, sessionID *string) (string, error) {
	if sessionID == nil || *sessionID == "" {
		return "", nil
	}
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		return "", nil
	}
	si, err := sm.LoadInfo(ctx, *sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if si.AgentID != agentID || si.ProjectID == "" {
		return "", nil
	}
	p, err := s.q.GetProject(ctx, sqlc.GetProjectParams{ID: si.ProjectID, UserID: si.UserID})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return p.BaseDir, nil
}

func dbSkillsToPluginSkills(rows []skills.Skill) []pkgplugins.Skill {
	out := make([]pkgplugins.Skill, len(rows))
	for i, r := range rows {
		out[i] = pkgplugins.Skill{
			ID: r.ID, Scope: r.Scope, UserID: r.UserID, AgentID: r.AgentID,
			Name: r.Name, Description: r.Description, Status: r.Status,
			DisableModelInvocation: r.DisableModelInvocation, Metadata: r.Metadata,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
	}
	return out
}

func resolvedSkillToView(rs skillstool.ResolvedSkill) skillView {
	var files []string
	if rs.Dir != "" {
		files, _ = skillstool.ListDirFiles(rs.Dir)
	}
	if files == nil {
		files = []string{}
	}
	return skillView{
		ID:                     rs.ID,
		Scope:                  rs.Scope,
		UserID:                 rs.UserID,
		AgentID:                rs.AgentID,
		Name:                   rs.Name,
		Description:            rs.Description,
		Status:                 rs.Status,
		DisableModelInvocation: rs.DisableModelInvocation,
		Files:                  files,
		CreatedAt:              rs.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:              rs.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// requireAgentSkillWrite checks auth for write operations on DB-backed scopes.
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
	case "project":
		return "", skills.ViewContext{}, http.StatusBadRequest, "project skills are managed via the CLI or filesystem"
	case "system":
		return "", skills.ViewContext{}, http.StatusForbidden, "system skills are read-only"
	default:
		return "", skills.ViewContext{}, http.StatusBadRequest, "scope must be one of: project, system, agent, user"
	}
}

func safeSkillFilePath(skillDir, filePath string) (string, error) {
	clean := filepath.Clean(filePath)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fs.ErrPermission
	}
	return filepath.Join(skillDir, clean), nil
}

// resolveSkillAny finds a skill by name across all scopes (highest priority wins).
func (s *Server) resolveSkillAny(ctx context.Context, agentID, skillName string, sessionID *string) (*skillstool.ResolvedSkill, string, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return nil, "", http.StatusUnauthorized, "unauthorized"
	}
	if _, code, msg := s.requireAgentAccess(ctx, agentID); code != 0 {
		return nil, "", code, msg
	}
	projectRoot, _ := s.projectRootForSession(memoryContext2(ctx), agentID, sessionID)
	vc := pkgplugins.SkillViewContext{UserID: info.UserID, AgentID: agentID}
	rs, err := s.skillService().Resolve(ctx, skillName, vc, projectRoot)
	if err != nil {
		return nil, "", http.StatusInternalServerError, err.Error()
	}
	if rs == nil {
		return nil, "", http.StatusNotFound, "skill not found"
	}
	return rs, projectRoot, 0, ""
}

// resolveSkill finds a skill by name in a specific scope for the given agent.
func (s *Server) resolveSkill(ctx context.Context, agentID, skillName, scope string, sessionID *string) (*skillstool.ResolvedSkill, string, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return nil, "", http.StatusUnauthorized, "unauthorized"
	}
	if _, code, msg := s.requireAgentAccess(ctx, agentID); code != 0 {
		return nil, "", code, msg
	}
	projectRoot, _ := s.projectRootForSession(memoryContext2(ctx), agentID, sessionID)
	vc := pkgplugins.SkillViewContext{UserID: info.UserID, AgentID: agentID}
	rs, err := s.skillService().ResolveScoped(ctx, skillName, scope, vc, projectRoot)
	if err != nil {
		return nil, "", http.StatusInternalServerError, err.Error()
	}
	if rs == nil {
		return nil, "", http.StatusNotFound, "skill not found"
	}
	return rs, projectRoot, 0, ""
}

// memoryContext2 builds memory context from just ctx (no *http.Request).
func memoryContext2(ctx context.Context) context.Context {
	return ctx
}

// loadSkillFile loads a file from an already-resolved skill.
func (s *Server) loadSkillFile(ctx context.Context, rs *skillstool.ResolvedSkill, path string) (string, error) {
	if rs.Dir != "" {
		fp, err := safeSkillFilePath(rs.Dir, path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(fp)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	store := s.skillStore()
	if store == nil {
		return "", errors.New("skills store not available")
	}
	return store.LoadFile(ctx, rs.ID, path)
}

// ---- Agent skills: /api/agents/{id}/skills* ---------------------------------

func (s *Server) ListAgentSkills(w http.ResponseWriter, r *http.Request, id string, params apiserver.ListAgentSkillsParams) {
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
	projectRoot, err := s.projectRootForSession(memoryContext(r, agentID), agentID, params.SessionId)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbSkills, err := s.skillStore().ListForAgentContext(r.Context(), info.UserID, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	merged := s.skillService().ListMergedWithDB(dbSkillsToPluginSkills(dbSkills), projectRoot)
	out := make([]skillView, 0, len(merged))
	for _, rs := range merged {
		if params.Scope != nil && rs.Scope != string(*params.Scope) {
			continue
		}
		out = append(out, resolvedSkillToView(rs))
	}
	writeListData(w, http.StatusOK, out)
}

func (s *Server) CreateAgentSkill(w http.ResponseWriter, r *http.Request, id string) {
	agentID := id
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
	userID, _, code, msg := s.requireAgentSkillWrite(r.Context(), agentID, req.Scope)
	if code != 0 {
		writeError(w, code, msg)
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
		Scope:                  req.Scope,
		AgentID:                agentID,
		Name:                   req.Name,
		Description:            req.Description,
		Status:                 req.Status,
		DisableModelInvocation: req.DisableModelInvocation,
	}
	if req.Scope == "user" {
		sk.UserID = userID
	}
	createdID, err := s.skillStore().Create(r.Context(), sk, files)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"id": createdID, "name": req.Name})
}

func (s *Server) GetAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.GetAgentSkillParams) {
	var rs *skillstool.ResolvedSkill
	var code int
	var msg string
	if params.Scope != nil {
		rs, _, code, msg = s.resolveSkill(r.Context(), id, skillId, string(*params.Scope), params.SessionId)
	} else {
		rs, _, code, msg = s.resolveSkillAny(r.Context(), id, skillId, params.SessionId)
	}
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	view := resolvedSkillToView(*rs)
	if rs.Dir == "" && s.skillStore() != nil {
		view.Files, _ = s.skillStore().ListFiles(r.Context(), rs.ID)
		if view.Files == nil {
			view.Files = []string{}
		}
	}
	writeData(w, http.StatusOK, view)
}

func (s *Server) UpdateAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.UpdateAgentSkillParams) {
	rs, _, code, msg := s.resolveSkill(r.Context(), id, skillId, string(params.Scope), params.SessionId)
	if code != 0 {
		writeError(w, code, msg)
		return
	}

	// Project/system skills live on the filesystem.
	if rs.Dir != "" {
		if rs.Scope == "system" {
			writeError(w, http.StatusForbidden, "system skills are read-only")
			return
		}
		var req updateSkillRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		for p, content := range req.Files {
			file, err := safeSkillFilePath(rs.Dir, p)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid path")
				return
			}
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		writeData(w, http.StatusOK, map[string]string{"id": skillId})
		return
	}

	// DB-backed skill.
	_, vc, code, msg := s.requireAgentSkillWrite(r.Context(), id, rs.Scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), rs.ID, rs.Scope, vc.UserID, id); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.applySkillUpdate(w, r, rs.ID, vc)
}

func (s *Server) DeleteAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.DeleteAgentSkillParams) {
	rs, _, code, msg := s.resolveSkill(r.Context(), id, skillId, string(params.Scope), params.SessionId)
	if code != 0 {
		writeError(w, code, msg)
		return
	}

	if rs.Dir != "" {
		if rs.Scope == "system" {
			writeError(w, http.StatusForbidden, "system skills are read-only")
			return
		}
		if err := os.RemoveAll(rs.Dir); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeNoContent(w)
		return
	}

	_, vc, code, msg := s.requireAgentSkillWrite(r.Context(), id, rs.Scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), rs.ID, rs.Scope, vc.UserID, id); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.doDeleteSkill(w, r, rs.ID, vc)
}

func (s *Server) GetAgentSkillFile(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.GetAgentSkillFileParams) {
	var rs *skillstool.ResolvedSkill
	var code int
	var msg string
	if params.Scope != nil {
		rs, _, code, msg = s.resolveSkill(r.Context(), id, skillId, string(*params.Scope), params.SessionId)
	} else {
		rs, _, code, msg = s.resolveSkillAny(r.Context(), id, skillId, params.SessionId)
	}
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	content, err := s.loadSkillFile(r.Context(), rs, params.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"path": params.Path, "content": content})
}

func (s *Server) DeleteAgentSkillFile(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.DeleteAgentSkillFileParams) {
	rs, _, code, msg := s.resolveSkill(r.Context(), id, skillId, string(params.Scope), params.SessionId)
	if code != 0 {
		writeError(w, code, msg)
		return
	}

	if rs.Dir != "" {
		if rs.Scope == "system" {
			writeError(w, http.StatusForbidden, "system skills are read-only")
			return
		}
		if params.Path == skills.MainFile {
			writeError(w, http.StatusBadRequest, "cannot delete SKILL.md")
			return
		}
		file, err := safeSkillFilePath(rs.Dir, params.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid path")
			return
		}
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeNoContent(w)
		return
	}

	_, vc, code, msg := s.requireAgentSkillWrite(r.Context(), id, rs.Scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), rs.ID, rs.Scope, vc.UserID, id); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.doDeleteSkillFile(w, r, rs.ID, params.Path)
}

func (s *Server) InstallAgentSkill(w http.ResponseWriter, r *http.Request, id string) {
	agentID := id
	var req installSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "source is required")
		return
	}
	scope := req.Scope
	if scope == "" {
		scope = "agent"
	}
	userID, _, code, msg := s.requireAgentSkillWrite(r.Context(), agentID, scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	storeUserID := ""
	if scope == "user" {
		storeUserID = userID
	}
	name, err := skillstool.InstallToStore(r.Context(), pluginhost.NewSkillStoreAdapter(s.skillStore()), req.Source, scope, storeUserID, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"name": name})
}

func (s *Server) UploadAgentSkill(w http.ResponseWriter, r *http.Request, id string) {
	s.uploadAgentSkill(w, r, id) //nolint:contextcheck
}
