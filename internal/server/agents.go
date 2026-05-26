package server

import (
	"fmt"
	"net/http"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/resources"
)

// createAgentRequest wraps config.Agent to accept an optional template_id
// hint. When set, fields on the template pre-populate empty fields on the
// submitted agent; values the user already supplied always win.
type createAgentRequest struct {
	config.Agent
	TemplateID string `json:"template_id"`
}

// applyTemplate merges a builtin template (and its referenced soul) into a
// fresh agent. Non-empty fields on the agent take precedence so the caller
// can override template defaults from the UI.
func applyTemplate(a *config.Agent, templateID string) error {
	if templateID == "" {
		return nil
	}
	reg, err := resources.Default()
	if err != nil {
		return fmt.Errorf("load builtin registry: %w", err)
	}
	tmpl, ok := reg.Get(resources.KindTemplate, templateID)
	if !ok {
		return fmt.Errorf("template %q not found", templateID)
	}
	if a.Model == "" {
		if model, ok := tmpl.Metadata["model"].(string); ok {
			a.Model = model
		}
	}
	if a.SystemPrompt == "" {
		a.SystemPrompt = tmpl.Content
	}
	if a.Soul == "" {
		soulID, _ := tmpl.Metadata["soul_id"].(string)
		if soulID != "" {
			if soul, ok := reg.Get(resources.KindSoul, soulID); ok {
				a.Soul = soul.Content
			}
		}
	}
	return nil
}

func (s *Server) ListAgents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info := UserFromContext(ctx)

	var (
		agents []config.Agent
		err    error
	)
	if info != nil && info.OrgID != "" {
		agents, err = s.store.ListAgentsForOrg(ctx, info.OrgID)
	} else {
		agents, err = s.store.ListAgents(ctx)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Admin sees all agents in scope. Non-admin users see only accessible agents.
	if info != nil && !info.IsAdmin {
		agents, err = s.filterAccessibleAgents(ctx, info, agents)
		if err != nil {
			s.log.Error("filter accessible agents", "user_id", info.UserID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to filter agents")
			return
		}
	}

	for i := range agents {
		fillAgentDefaults(&agents[i])
	}
	writeListData(w, http.StatusOK, agents)
}

func (s *Server) CreateAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info := UserFromContext(ctx)

	var req createAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	a := req.Agent
	if a.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := applyTemplate(&a, req.TemplateID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// backend is global; only network is per-agent (no Backend field to clear)
	if err := a.Sandbox.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Auto-generate ID from name.
	a.ID = slugify(a.Name)

	// Deduplicate: if the ID already exists, append a suffix.
	if _, err := s.store.GetAgent(ctx, a.ID); err == nil {
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s-%d", a.ID, i)
			if _, err := s.store.GetAgent(ctx, candidate); err != nil {
				a.ID = candidate
				break
			}
		}
	}

	// Workspace is always the default path — never user-supplied.
	a.Workspace = ""

	// Set creator.
	if info != nil {
		a.CreatorID = info.UserID
	}

	// Non-admin users always get restricted scope, auto-assigned.
	if info != nil && !info.IsAdmin {
		a.Scope = config.AgentScopeRestricted
	} else {
		if a.Scope == "" {
			a.Scope = config.AgentScopeSystem
		}
		if a.Scope != config.AgentScopeSystem && a.Scope != config.AgentScopeRestricted {
			writeError(w, http.StatusBadRequest, "scope must be 'system' or 'restricted'")
			return
		}
	}

	if err := s.store.CreateAgent(ctx, a); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Stamp org when the creating user belongs to one.
	if info != nil && info.OrgID != "" {
		if err := s.store.SetAgentOrg(ctx, a.ID, info.OrgID); err != nil {
			s.log.Error("stamp agent org", "agent_id", a.ID, "org_id", info.OrgID, "error", err)
		}
		a.OrgID = info.OrgID
	}

	if s.poolManager != nil {
		if err := s.poolManager.SyncAgent(ctx, a.ID); err != nil {
			s.log.Error("sync agent pool after create", "agent_id", a.ID, "error", err)
		}
	}

	// Auto-assign the creator if scope is restricted and user is non-admin.
	if info != nil && !info.IsAdmin && a.Scope == config.AgentScopeRestricted {
		if err := s.authStore.AssignAgent(ctx, info.UserID, a.ID); err != nil {
			s.log.Error("auto-assign agent to creator", "user_id", info.UserID, "agent_id", a.ID, "error", err)
		}
	}

	writeData(w, http.StatusCreated, a)
}

func (s *Server) GetAgent(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	info := UserFromContext(ctx)

	a, err := s.store.GetAgent(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	// Block cross-org access when the caller belongs to an org.
	if info != nil && info.OrgID != "" && a.OrgID != "" && a.OrgID != info.OrgID {
		writeError(w, http.StatusForbidden, "agent not found")
		return
	}

	// Non-admin users can only access system or assigned agents.
	if info != nil && !info.IsAdmin {
		if !s.canAccessAgent(ctx, info, a) {
			writeError(w, http.StatusForbidden, "you don't have access to this agent")
			return
		}
	}

	fillAgentDefaults(&a)
	writeData(w, http.StatusOK, a)
}

func (s *Server) UpdateAgent(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	info := UserFromContext(ctx)

	// Check access: admin or creator.
	existing, err := s.store.GetAgent(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	// Block cross-org write.
	if info != nil && info.OrgID != "" && existing.OrgID != "" && existing.OrgID != info.OrgID {
		writeError(w, http.StatusForbidden, "agent not found")
		return
	}
	if info != nil && !info.IsAdmin && existing.CreatorID != info.UserID {
		writeError(w, http.StatusForbidden, "only the creator can edit this agent")
		return
	}

	var a config.Agent
	if err := decodeJSON(r, &a); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	a.ID = id
	if a.Name == "" {
		a.Name = id
	}
	// backend is global; only network is per-agent (no Backend field to clear)
	if err := a.Sandbox.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Non-admin: keep scope as-is, don't allow changing it.
	if info != nil && !info.IsAdmin {
		a.Scope = existing.Scope
	} else {
		if a.Scope == "" {
			a.Scope = config.AgentScopeSystem
		}
		if a.Scope != config.AgentScopeSystem && a.Scope != config.AgentScopeRestricted {
			writeError(w, http.StatusBadRequest, "scope must be 'system' or 'restricted'")
			return
		}
	}

	if err := s.store.UpdateAgent(ctx, a); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.poolManager != nil {
		if err := s.poolManager.SyncAgent(ctx, a.ID); err != nil {
			s.log.Error("sync agent pool after update", "agent_id", a.ID, "error", err)
		}
	}
	writeData(w, http.StatusOK, a)
}

func (s *Server) DeleteAgent(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	info := UserFromContext(ctx)

	// Check access: creator only.
	existing, err := s.store.GetAgent(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	// Block cross-org delete.
	if info != nil && info.OrgID != "" && existing.OrgID != "" && existing.OrgID != info.OrgID {
		writeError(w, http.StatusForbidden, "agent not found")
		return
	}
	if info != nil && existing.CreatorID != info.UserID {
		writeError(w, http.StatusForbidden, "only the creator can delete this agent")
		return
	}

	if err := s.store.DeleteAgent(ctx, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.poolManager != nil {
		if err := s.poolManager.SyncAgent(ctx, id); err != nil {
			s.log.Error("sync agent pool after delete", "agent_id", id, "error", err)
		}
	}
	writeNoContent(w)
}
