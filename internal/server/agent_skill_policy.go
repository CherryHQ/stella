package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agentskillpolicy"
	"github.com/CherryHQ/stella/internal/skillaccess"
	"github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/resources"
)

// AgentSkillPolicyStore is the narrow persistence port for the Agent setting;
// it intentionally is not part of Skill content storage or authorization.
type AgentSkillPolicyStore interface {
	ReadAgentSkillPolicy(context.Context, string) (agentskillpolicy.Policy, agentskillpolicy.Diagnostics, error)
	SetAgentSkillPolicy(context.Context, string, string, bool) (agentskillpolicy.Policy, error)
}

func (s *Server) UpdateAgentSkillActivation(w http.ResponseWriter, r *http.Request, id string, skillRef string) {
	if s.agentSkillPolicy == nil {
		writeError(w, http.StatusServiceUnavailable, "Agent Skill policy unavailable")
		return
	}
	if _, code, msg := s.requireAgentManage(r.Context(), id); code != 0 {
		writeError(w, code, msg)
		return
	}
	if err := agentskillpolicy.ValidateRef(skillRef); err != nil {
		writeError(w, http.StatusBadRequest, "invalid skill ref")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	policy, _, err := s.agentSkillPolicy.ReadAgentSkillPolicy(r.Context(), id)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	storedDangling := policy.DisabledRef(skillRef)
	exists, err := s.policyRefExists(r.Context(), id, skillRef)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	if (!*req.Enabled || !storedDangling) && !exists {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}
	mutate := func() error {
		var mutationErr error
		policy, mutationErr = s.agentSkillPolicy.SetAgentSkillPolicy(r.Context(), id, skillRef, *req.Enabled)
		return mutationErr
	}
	if s.poolManager != nil {
		err = s.poolManager.ApplyAgentSkillPolicyMutation(id, mutate)
	} else {
		err = mutate()
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"logical_ref": skillRef, "enabled": !policy.DisabledRef(skillRef)})
}

// policyRefExists separates a missing Skill from a failed catalog read. The
// latter must reach the caller as a 500, never as a misleading clean 404.
func (s *Server) policyRefExists(ctx context.Context, agentID, ref string) (bool, error) {
	scope, name, _ := strings.Cut(ref, ":")
	switch scope {
	case "builtin":
		registry, err := resources.Default()
		if err != nil {
			return false, fmt.Errorf("load builtin Skill catalog: %w", err)
		}
		_, ok := registry.BuiltinSkill(name)
		return ok, nil
	case "system", "system_agent":
		if s.skillStore() == nil {
			return false, errors.New("skills store unavailable")
		}
		acc, code, msg := s.beginAgentSkillAccess(ctx, agentID)
		if code != 0 {
			return false, fmt.Errorf("authorize Agent Skill policy catalog: %s", msg)
		}
		agent := ""
		if scope == "system_agent" {
			agent = agentID
		}
		var rows []skills.Skill
		var err error
		if reader, ok := s.skillStore().(skills.IdentityReader); ok {
			rows, err = reader.ListIdentityByScope(ctx, scope, "", agent)
			if err != nil {
				return false, fmt.Errorf("list %s Skill identities: %w", scope, err)
			}
			for i := range rows {
				if rows[i].Name != name {
					continue
				}
				if authErr := acc.AuthorizeRead(ctx, rows[i]); authErr != nil {
					if errors.Is(authErr, skillaccess.ErrNotFound) || errors.Is(authErr, skillaccess.ErrForbidden) {
						continue
					}
					return false, authErr
				}
				revision, loadErr := reader.LoadCurrentRevision(ctx, rows[i])
				if skills.IsCurrentSelectorMissing(loadErr) {
					s.warnMissingSkillSelector(rows[i], loadErr)
					continue
				}
				if loadErr != nil {
					return false, loadErr
				}
				if revision.Skill.Name == name && revision.Skill.Status != skills.SkillStatusDeprecated {
					return true, nil
				}
			}
			return false, nil
		} else {
			rows, err = s.skillStore().ListByScope(ctx, scope, "", agent)
		}
		if err != nil {
			return false, fmt.Errorf("list %s Skills: %w", scope, err)
		}
		for _, row := range rows {
			if row.Name == name && row.Status != skills.SkillStatusDeprecated {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Server) contextualSkillView(rs skills.ResolvedSkill, policy agentskillpolicy.Policy) skillView {
	view := resolvedSkillToView(rs)
	if rs.BuiltinFiles() != nil {
		view.Scope = "builtin"
		builtin := true
		view.Builtin = &builtin
	}
	enabled := true
	if ref, ok := skills.PolicyRef(rs); ok {
		view.LogicalRef = ref
		enabled = !policy.DisabledRef(ref)
	}
	view.Enabled = &enabled
	return view
}
