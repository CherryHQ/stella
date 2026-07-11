package server

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
)

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

// canExecuteAgent checks if the user may run a specific agent.
func (s *Server) canExecuteAgent(ctx context.Context, info *AuthInfo, a config.Agent) bool {
	subject, err := s.agentAccessSubject(ctx, info)
	if err != nil {
		s.log.Error("list user agent IDs for execute check", "user_id", info.UserID, "error", err)
		return false
	}
	return s.engine.Can(ctx, s.agentExecuteRequest(subject, a))
}

func (s *Server) agentExecuteRequest(subject auth.Subject, a config.Agent) auth.AccessRequest {
	return auth.AccessRequest{
		Subject: subject,
		Action:  auth.ActionExecute,
		Resource: auth.Resource{
			Type:  auth.ResourceAgent,
			ID:    a.ID,
			Attrs: map[string]any{"scope": a.Scope},
		},
	}
}
