package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/pluginhost"
	"github.com/vaayne/anna/internal/skills"
	skillstool "github.com/vaayne/anna/plugins/tools/skills"
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

// requireAgentManage verifies the caller is admin or the agent's creator.
// Returns (agent, 0, "") on success, (_, status, msg) on failure.
func (s *Server) requireAgentManage(ctx context.Context, agentID string) (config.Agent, int, string) {
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
	if !info.IsAdmin && a.CreatorID != info.UserID {
		return config.Agent{}, http.StatusForbidden, "forbidden"
	}
	return a, 0, ""
}

// requireSkillScope checks that skill matches the expected scope and owner.
// Returns the skill or an error status + message.
func (s *Server) requireSkillScope(ctx context.Context, id, scope string, userID int64, agentID string) (*skills.Skill, int, string) {
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
		if sk.UserID != userID {
			return nil, http.StatusNotFound, "skill not found"
		}
	}
	return sk, 0, ""
}

// ---- Agent-scoped skills: /api/agents/{id}/skills* ----

func (s *Server) listAgentSkills(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, code, msg := s.requireAgentManage(r.Context(), agentID); code != 0 {
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
		if sk.Scope == "agent" && sk.AgentID == agentID {
			out = append(out, skillToView(sk, nil))
		}
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) getAgentSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	skillID := r.PathValue("skillId")
	if _, code, msg := s.requireAgentManage(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	sk, code, msg := s.requireSkillScope(r.Context(), skillID, "agent", 0, agentID)
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

func (s *Server) getAgentSkillFile(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	skillID := r.PathValue("skillId")
	if _, code, msg := s.requireAgentManage(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), skillID, "agent", 0, agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.serveSkillFile(w, r, skillID)
}

func (s *Server) updateAgentSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	skillID := r.PathValue("skillId")
	if _, code, msg := s.requireAgentManage(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), skillID, "agent", 0, agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.applySkillUpdate(w, r, skillID)
}

func (s *Server) deleteAgentSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	skillID := r.PathValue("skillId")
	if _, code, msg := s.requireAgentManage(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), skillID, "agent", 0, agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.doDeleteSkill(w, r, skillID)
}

func (s *Server) deleteAgentSkillFile(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	skillID := r.PathValue("skillId")
	if _, code, msg := s.requireAgentManage(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), skillID, "agent", 0, agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.doDeleteSkillFile(w, r, skillID)
}

func (s *Server) installAgentSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
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
	name, err := skillstool.InstallToStore(r.Context(), pluginhost.NewSkillStoreAdapter(s.skillStore()), req.Source, "agent", 0, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"name": name})
}

// ---- Profile (self-user) skills: /api/auth/profile/skills* ----

func (s *Server) listProfileSkills(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) getProfileSkill(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	skillID := r.PathValue("skillId")
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

func (s *Server) getProfileSkillFile(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	skillID := r.PathValue("skillId")
	if _, code, msg := s.requireSkillScope(r.Context(), skillID, "user", info.UserID, ""); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.serveSkillFile(w, r, skillID)
}

func (s *Server) updateProfileSkill(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	skillID := r.PathValue("skillId")
	if _, code, msg := s.requireSkillScope(r.Context(), skillID, "user", info.UserID, ""); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.applySkillUpdate(w, r, skillID)
}

func (s *Server) deleteProfileSkill(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	skillID := r.PathValue("skillId")
	if _, code, msg := s.requireSkillScope(r.Context(), skillID, "user", info.UserID, ""); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.doDeleteSkill(w, r, skillID)
}

func (s *Server) deleteProfileSkillFile(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	skillID := r.PathValue("skillId")
	if _, code, msg := s.requireSkillScope(r.Context(), skillID, "user", info.UserID, ""); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.doDeleteSkillFile(w, r, skillID)
}

func (s *Server) installProfileSkill(w http.ResponseWriter, r *http.Request) {
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
