package webhook

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/credential"
)

type UserStateFromLookup struct{ lookup credential.UserLookup }

func NewUserState(lookup credential.UserLookup) UserStateFromLookup {
	return UserStateFromLookup{lookup: lookup}
}

func (s UserStateFromLookup) IsActive(ctx context.Context, userID string) (bool, error) {
	identity, err := s.lookup.LookupUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return identity.IsActive, nil
}

type UserAgentAccessFromService struct{ access *agentaccess.Service }

func NewUserAgentAccess(access *agentaccess.Service) UserAgentAccessFromService {
	return UserAgentAccessFromService{access: access}
}

func (a UserAgentAccessFromService) CanUseUser(ctx context.Context, userID, agentID string) (bool, error) {
	if a.access == nil {
		return false, errors.New("webhook: agent access is unavailable")
	}
	allowed, err := a.access.CanUseAsUser(ctx, userID, agentID)
	if errors.Is(err, agentaccess.ErrForbidden) || errors.Is(err, agentaccess.ErrNotFound) {
		return false, nil
	}
	return allowed, err
}
