package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	apiserver "github.com/CherryHQ/stella/api/server"
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
func validThinkingLevel(level string) bool {
	switch level {
	case "", "minimal", "low", "medium", "high", "xhigh":
		return true
	default:
		return false
	}
}

func validateAgentThinking(a config.Agent) bool {
	return validThinkingLevel(a.ModelThinking) &&
		validThinkingLevel(a.ModelStrongThinking) &&
		validThinkingLevel(a.ModelFastThinking)
}

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

func (s *Server) ListAgents(w http.ResponseWriter, r *http.Request, params apiserver.ListAgentsParams) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	ctx := r.Context()

	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	includeAll := info.IsAdmin && params.IncludeAll != nil && *params.IncludeAll
	agents, err := s.agentAccess.ListReadable(ctx, authority, includeAll)
	if err != nil {
		code, msg := agentAccessError(err)
		writeError(w, code, msg)
		return
	}

	for i := range agents {
		fillAgentDefaults(&agents[i])
	}
	if err := s.fillAgentLastActive(ctx, info.UserID, agents); err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) fillAgentLastActive(ctx context.Context, userID string, agents []config.Agent) error {
	rows, err := s.q.ListAgentConversationLastActive(ctx, pgtype.Text{String: userID, Valid: true})
	if err != nil {
		return err
	}
	byAgent := make(map[string]*time.Time, len(rows))
	for _, row := range rows {
		if !row.AgentID.Valid {
			continue
		}
		t := parseSQLValueTime(row.LastActive)
		if t.IsZero() {
			continue
		}
		byAgent[row.AgentID.String] = &t
	}
	for i := range agents {
		agents[i].LastActive = byAgent[agents[i].ID]
	}
	return nil
}

func (s *Server) CreateAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := s.agentAccess.CanCreate(ctx, authority); err != nil {
		code, msg := agentAccessError(err)
		writeError(w, code, msg)
		return
	}

	var req createAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	a := req.Agent
	if a.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := applyTemplate(&a, req.TemplateID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !validateAgentThinking(a) {
		writeError(w, http.StatusBadRequest, "invalid thinking level")
		return
	}
	// backend is global; only network is per-agent (no Backend field to clear)
	if err := a.Sandbox.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
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

	a.CreatorID = info.UserID

	// Non-admin users always get restricted scope, auto-assigned.
	if !info.IsAdmin {
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
		s.writeInternalError(w, err)
		return
	}

	if s.poolManager != nil {
		if err := s.poolManager.SyncAgent(ctx, a.ID); err != nil {
			s.log.Error("sync agent pool after create", "agent_id", a.ID, "error", err)
		}
	}

	// Auto-assign the creator if scope is restricted and user is non-admin.
	if !info.IsAdmin && a.Scope == config.AgentScopeRestricted {
		if err := s.authStore.AssignAgent(ctx, info.UserID, a.ID); err != nil {
			s.log.Error("auto-assign agent to creator", "user_id", info.UserID, "agent_id", a.ID, "error", err)
		}
	}

	writeData(w, http.StatusCreated, a)
}

func (s *Server) GetAgent(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	a, code, msg := s.requireAgentAccess(ctx, id)
	if code != 0 {
		writeError(w, code, msg)
		return
	}

	fillAgentDefaults(&a)
	writeData(w, http.StatusOK, a)
}

func (s *Server) UpdateAgent(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	info := UserFromContext(ctx)

	existing, code, msg := s.requireAgentManage(ctx, id)
	if code != 0 {
		writeError(w, code, msg)
		return
	}

	var a config.Agent
	if err := decodeJSON(r, &a); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	a.ID = id
	if a.Name == "" {
		a.Name = id
	}
	if !validateAgentThinking(a) {
		writeError(w, http.StatusBadRequest, "invalid thinking level")
		return
	}
	// backend is global; only network is per-agent (no Backend field to clear)
	if err := a.Sandbox.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	// Non-admin: keep scope as-is, don't allow changing it.
	if !info.IsAdmin {
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
		s.writeInternalError(w, err)
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
	if _, code, msg := s.requireAgentDelete(ctx, id); code != 0 {
		writeError(w, code, msg)
		return
	}

	if err := s.store.DeleteAgent(ctx, id); err != nil {
		s.writeInternalError(w, err)
		return
	}
	if s.poolManager != nil {
		if err := s.poolManager.SyncAgent(ctx, id); err != nil {
			s.log.Error("sync agent pool after delete", "agent_id", id, "error", err)
		}
	}
	writeNoContent(w)
}
