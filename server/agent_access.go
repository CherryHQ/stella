package server

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
)

// filterAccessibleAgents returns only agents the user can access (system scope + assigned).
func (s *Server) filterAccessibleAgents(ctx context.Context, info *AuthInfo, agents []config.Agent) ([]config.Agent, error) {
	subject, err := s.agentAccessSubject(ctx, info)
	if err != nil {
		return nil, err
	}

	var filtered []config.Agent
	for _, a := range agents {
		if s.engine.Can(ctx, s.agentReadRequest(subject, a)) {
			filtered = append(filtered, a)
		}
	}
	return filtered, nil
}

// canAccessAgent checks if the user can access a specific agent.
func (s *Server) canAccessAgent(ctx context.Context, info *AuthInfo, a config.Agent) bool {
	subject, err := s.agentAccessSubject(ctx, info)
	if err != nil {
		s.log.Error("list user agent IDs for access check", "user_id", info.UserID, "error", err)
		return false
	}
	return s.engine.Can(ctx, s.agentReadRequest(subject, a))
}

func (s *Server) agentAccessSubject(ctx context.Context, info *AuthInfo) (auth.Subject, error) {
	assignedIDs, err := s.authStore.ListUserAgentIDs(ctx, info.UserID)
	if err != nil {
		return auth.Subject{}, fmt.Errorf("list user agent IDs: %w", err)
	}

	return auth.Subject{
		UserID:   info.UserID,
		Roles:    []string{info.Role},
		AgentIDs: assignedIDs,
	}, nil
}

func (s *Server) agentReadRequest(subject auth.Subject, a config.Agent) auth.AccessRequest {
	return auth.AccessRequest{
		Subject: subject,
		Action:  auth.ActionRead,
		Resource: auth.Resource{
			Type:  auth.ResourceAgent,
			ID:    a.ID,
			Attrs: map[string]any{"scope": a.Scope},
		},
	}
}
