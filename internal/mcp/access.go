package mcp

import (
	"context"
	"errors"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
)

// Access binds MCP scope ownership to a verified Authority. HTTP and the
// model-facing Settings tool use this same seam; raw Service methods are kept
// for trusted runtime reads and are not an authorization boundary.
type Access struct {
	svc       *Service
	agents    *agentaccess.Service
	authority authz.Authority
}

func NewAccess(svc *Service, agents *agentaccess.Service) *Access {
	return &Access{svc: svc, agents: agents}
}

func (s *Access) Begin(authority authz.Authority) (*Access, error) {
	if s == nil || s.svc == nil || !authority.Valid() || authority.Kind() != authz.ActorUser {
		return nil, authz.ErrForbidden
	}
	return &Access{svc: s.svc, agents: s.agents, authority: authority}, nil
}

func (a *Access) owner(ctx context.Context, scope, agentID string) (string, string, error) {
	if a == nil || a.authority.Kind() != authz.ActorUser {
		return "", "", authz.ErrForbidden
	}
	userID := string(a.authority.UserID())
	switch scope {
	case ScopeUser:
		if agentID != "" {
			return "", "", errors.New("mcp: user scope cannot include agent_id")
		}
		return userID, "", nil
	case ScopeUserAgent:
		if agentID == "" {
			return "", "", errors.New("mcp: user_agent scope requires agent_id")
		}
		if a.agents == nil {
			return "", "", authz.ErrForbidden
		}
		if err := a.agents.Authorize(ctx, a.authority, agentID, authz.ActionRead); err != nil {
			return "", "", err
		}
		return userID, agentID, nil
	case ScopeSystem:
		if !a.authority.IsAdmin() || agentID != "" {
			return "", "", authz.ErrForbidden
		}
		return "", "", nil
	case ScopeSystemAgent:
		if !a.authority.IsAdmin() || agentID == "" {
			return "", "", authz.ErrForbidden
		}
		if a.agents == nil {
			return "", "", authz.ErrForbidden
		}
		if err := a.agents.Authorize(ctx, a.authority, agentID, authz.ActionRead); err != nil {
			return "", "", err
		}
		return "", agentID, nil
	default:
		return "", "", errors.New("mcp: invalid scope")
	}
}

func (a *Access) List(ctx context.Context, scope, agentID string) ([]Registration, error) {
	uid, aid, err := a.owner(ctx, scope, agentID)
	if err != nil {
		return nil, err
	}
	return a.svc.ListByScope(ctx, scope, uid, aid)
}

func (a *Access) Create(ctx context.Context, in CreateInput) (Registration, error) {
	uid, aid, err := a.owner(ctx, in.Scope, in.AgentID)
	if err != nil {
		return Registration{}, err
	}
	in.UserID, in.AgentID = uid, aid
	return a.svc.Create(ctx, in)
}

func (a *Access) Update(ctx context.Context, in UpdateInput) (Registration, error) {
	uid, aid, err := a.owner(ctx, in.Scope, in.AgentID)
	if err != nil {
		return Registration{}, err
	}
	in.UserID, in.AgentID = uid, aid
	if in.NewScope != nil {
		newUID, newAID, err := a.owner(ctx, *in.NewScope, in.NewAgentID)
		if err != nil {
			return Registration{}, err
		}
		in.NewUserID, in.NewAgentID = newUID, newAID
	}
	return a.svc.Update(ctx, in)
}

func (a *Access) Delete(ctx context.Context, id, scope, agentID string) error {
	uid, aid, err := a.owner(ctx, scope, agentID)
	if err != nil {
		return err
	}
	return a.svc.Delete(ctx, id, scope, uid, aid)
}
