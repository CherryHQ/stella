package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
)

// Access binds registration ownership to a verified user authority. Raw Service
// methods remain available to the runtime, which reads effective registrations,
// but HTTP and Stella Settings must use this boundary.
type Access struct {
	svc       *Service
	agents    *agentaccess.Service
	pools     *agent.PoolManager
	authority authz.Authority
}

func NewAccess(svc *Service, agents *agentaccess.Service, pools *agent.PoolManager) *Access {
	return &Access{svc: svc, agents: agents, pools: pools}
}

func (s *Access) Begin(authority authz.Authority) (*Access, error) {
	if s == nil || s.svc == nil || !authority.Valid() || authority.Kind() != authz.ActorUser {
		return nil, authz.ErrForbidden
	}
	return &Access{svc: s.svc, agents: s.agents, pools: s.pools, authority: authority}, nil
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
		if !a.authority.IsAdmin() || agentID == "" || a.agents == nil {
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

func (a *Access) Get(ctx context.Context, id, scope, agentID string) (Registration, error) {
	uid, aid, err := a.owner(ctx, scope, agentID)
	if err != nil {
		return Registration{}, err
	}
	return a.svc.Get(ctx, id, scope, uid, aid)
}

func (a *Access) Create(ctx context.Context, in CreateInput) (Registration, error) {
	uid, aid, err := a.owner(ctx, in.Scope, in.AgentID)
	if err != nil {
		return Registration{}, err
	}
	in.UserID, in.AgentID = uid, aid
	reg, err := a.svc.Create(ctx, in)
	if err != nil {
		return Registration{}, err
	}
	a.invalidate(reg.Scope, reg.UserID, reg.AgentID)
	return reg, nil
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
	reg, err := a.svc.Update(ctx, in)
	if err != nil {
		return Registration{}, err
	}
	a.invalidate(in.Scope, uid, aid)
	a.invalidate(reg.Scope, reg.UserID, reg.AgentID)
	return reg, nil
}

func (a *Access) Delete(ctx context.Context, id, scope, agentID string) error {
	uid, aid, err := a.owner(ctx, scope, agentID)
	if err != nil {
		return err
	}
	if err := a.svc.Delete(ctx, id, scope, uid, aid); err != nil {
		return err
	}
	a.invalidate(scope, uid, aid)
	return nil
}

func (a *Access) invalidate(scope, userID, agentID string) {
	if a.pools == nil {
		return
	}
	var err error
	switch scope {
	case ScopeUser:
		err = a.pools.InvalidateUser(userID)
	case ScopeUserAgent:
		err = a.pools.InvalidateUserAgent(userID, agentID)
	case ScopeSystemAgent:
		err = a.pools.InvalidateAgent(agentID)
	case ScopeSystem:
		err = a.pools.InvalidateAll()
	}
	if err != nil { // Invalidation is a cache refresh; committed DB state remains authoritative.
		return
	}
}

// Version is intentionally a stable opaque digest of redacted metadata. The
// credential reference and bearer never leave this package, yet any credential
// configuration change still invalidates a stale management write.
func (r Registration) Version() string {
	return fmt.Sprintf("%x", registrationHash(r))
}
