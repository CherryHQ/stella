package knowledge

import (
	"context"
	"errors"
	"fmt"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
)

// ResolveManageOwner authorizes a scope-keyed management operation and returns
// the exact owner tuple that may be passed to List or Create. HTTP parameters
// select a scope; they never supply the owning user, which always comes from the
// trusted Authority.
func (s *Service) ResolveManageOwner(
	ctx context.Context,
	authority authz.Authority,
	scope Scope,
	agentID string,
) (Owner, error) {
	if s == nil || !authority.Valid() || authority.Kind() != authz.ActorUser {
		return Owner{}, ErrForbidden
	}

	userID := string(authority.UserID())
	owner := Owner{Scope: scope}
	switch scope {
	case ScopeUser:
		if agentID != "" {
			return Owner{}, ErrInvalidOwner
		}
		owner.UserID = userID
	case ScopeUserAgent:
		if agentID == "" {
			return Owner{}, ErrInvalidOwner
		}
		owner.UserID = userID
		owner.AgentID = agentID
	case ScopeSystem:
		if agentID != "" {
			return Owner{}, ErrInvalidOwner
		}
		if !authority.IsAdmin() {
			return Owner{}, ErrForbidden
		}
	case ScopeSystemAgent:
		if agentID == "" {
			return Owner{}, ErrInvalidOwner
		}
		if !authority.IsAdmin() {
			return Owner{}, ErrForbidden
		}
		owner.AgentID = agentID
	default:
		return Owner{}, ErrInvalidOwner
	}

	if owner.AgentID != "" {
		if s.agentAccess == nil {
			return Owner{}, ErrServiceUnavailable
		}
		if _, err := s.agentAccess.Read(ctx, authority, owner.AgentID); err != nil {
			return Owner{}, knowledgeAgentAccessError(err)
		}
	}
	if err := owner.Validate(); err != nil {
		return Owner{}, err
	}
	return owner, nil
}

// GetManaged loads a file and authorizes the management view against its durable
// owner tuple. Every denial is opaque so knowing a foreign UUID cannot reveal
// whether that file exists.
func (s *Service) GetManaged(
	ctx context.Context,
	authority authz.Authority,
	id string,
) (File, error) {
	file, err := s.Get(ctx, id)
	if err != nil {
		return File{}, err
	}
	owner, err := s.ResolveManageOwner(ctx, authority, file.Owner.Scope, file.Owner.AgentID)
	switch {
	case err == nil:
	case errors.Is(err, ErrServiceUnavailable):
		return File{}, err
	default:
		return File{}, ErrNotFound
	}
	if owner != file.Owner {
		return File{}, ErrNotFound
	}
	return file, nil
}

func knowledgeAgentAccessError(err error) error {
	switch {
	case errors.Is(err, agentaccess.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, agentaccess.ErrForbidden):
		return ErrForbidden
	default:
		return fmt.Errorf("%w: agent access: %w", ErrServiceUnavailable, err)
	}
}
