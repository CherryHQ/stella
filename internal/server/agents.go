package server

import (
	"fmt"
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/resources"
)

// applyTemplate merges a builtin template (and its referenced soul) into a
// fresh agent. Non-empty fields on the agent take precedence so the caller
// can override template defaults from the UI.
func validateAgentThinking(a config.Agent) bool {
	return config.ValidThinkingLevel(a.ModelThinking) &&
		config.ValidThinkingLevel(a.ModelStrongThinking) &&
		config.ValidThinkingLevel(a.ModelFastThinking)
}

// validateAgentModels returns the field name and value of the first unusable
// model tier, or "" when every tier is usable. Only shape is checked, not
// provider existence: a provider row can be deleted after an agent references
// it, so "this provider is configured" is not an invariant a write-time check
// can hold, and a fresh deployment legitimately creates agents before any
// provider exists. An empty tier is valid and inherits the deployment default.
func validateAgentModels(a config.Agent) (field, value string) {
	for _, tier := range []struct{ field, value string }{
		{"model", a.Model},
		{"model_strong", a.ModelStrong},
		{"model_fast", a.ModelFast},
	} {
		if !config.ValidModelRef(tier.value) {
			return tier.field, tier.value
		}
	}
	return "", ""
}

func invalidModelMessage(field, value string) string {
	return fmt.Sprintf("invalid %s %q: expected \"provider/model\"", field, value)
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
		a.SystemPrompt = prompt.NamePersona(tmpl.Content, a.Name)
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
	// include_all is the admin's deployment-wide view. The default list is the
	// caller's own fleet for everyone, admin included: reaching every agent is
	// not a reason to be shown every agent.
	deploymentWide := info.IsAdmin && params.IncludeAll != nil && *params.IncludeAll
	agents, err := s.agentAccess.ListReadable(ctx, authority, deploymentWide)
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
	result := make([]apitypes.Agent, len(agents))
	for i := range agents {
		result[i] = agentToAPI(agents[i], viewerFrom(info))
	}
	writeData(w, http.StatusOK, apitypes.AgentList{Agents: result})
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

	var req apiserver.CreateAgentJSONRequestBody
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	a, templateID, credentials := createAgentFromAPI(req)
	if a.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := applyTemplate(&a, templateID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !validateAgentThinking(a) {
		writeError(w, http.StatusBadRequest, "invalid thinking level")
		return
	}
	if field, value := validateAgentModels(a); field != "" {
		writeError(w, http.StatusBadRequest, invalidModelMessage(field, value))
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

	created, err := s.agentManagement.CreateWithProviderCredentials(ctx, authority, a, credentials)
	if err != nil {
		code, msg := agentManagementError(err)
		if code == http.StatusInternalServerError {
			s.writeInternalError(w, err)
			return
		}
		writeError(w, code, msg)
		return
	}

	writeData(w, http.StatusCreated, agentToAPI(created, viewerFrom(info)))
}

func (s *Server) GetAgent(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	a, code, msg := s.requireAgentAccess(ctx, id)
	if code != 0 {
		writeError(w, code, msg)
		return
	}

	fillAgentDefaults(&a)
	writeData(w, http.StatusOK, agentToAPI(a, viewerFrom(UserFromContext(ctx))))
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

	var req apiserver.UpdateAgentJSONRequestBody
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	existing, code, msg := s.requireAgentManage(ctx, id)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	a := applyAgentPatch(existing, req)
	a.ID = id
	if a.Name == "" {
		a.Name = id
	}
	if !validateAgentThinking(a) {
		writeError(w, http.StatusBadRequest, "invalid thinking level")
		return
	}
	if field, value := validateAgentModels(a); field != "" {
		writeError(w, http.StatusBadRequest, invalidModelMessage(field, value))
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
	writeData(w, http.StatusOK, agentToAPI(updated, viewerFrom(info)))
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
