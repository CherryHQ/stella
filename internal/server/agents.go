package server

import (
	"fmt"
	"net/http"

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
	lastActive, err := s.agentManagement.ListAgentLastActive(ctx, info.UserID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	for i := range agents {
		if t, ok := lastActive[agents[i].ID]; ok {
			t := t
			agents[i].LastActive = &t
		}
	}
	writeData(w, http.StatusOK, map[string]any{"agents": agents})
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

	// Base ID slug from the display name; the Agent domain uniquifies it, owns the
	// scope decision, sets the creator, and (for a restricted agent) auto-assigns.
	a.ID = slugify(a.Name)

	created, err := s.agentManagement.Create(ctx, authority, a)
	if err != nil {
		code, msg := agentManagementError(err)
		if code == http.StatusInternalServerError {
			s.writeInternalError(w, err)
			return
		}
		writeError(w, code, msg)
		return
	}

	writeData(w, http.StatusCreated, created)
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
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
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

	updated, err := s.agentManagement.Update(ctx, authority, a)
	if err != nil {
		code, msg := agentManagementError(err)
		if code == http.StatusInternalServerError {
			s.writeInternalError(w, err)
			return
		}
		writeError(w, code, msg)
		return
	}
	writeData(w, http.StatusOK, updated)
}

func (s *Server) DeleteAgent(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	info := UserFromContext(ctx)
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	if err := s.agentManagement.Delete(ctx, authority, id); err != nil {
		code, msg := agentManagementError(err)
		if code == http.StatusInternalServerError {
			s.writeInternalError(w, err)
			return
		}
		writeError(w, code, msg)
		return
	}
	writeNoContent(w)
}
