package webhook

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/credential"
)

// UserStateFromLookup adapts the established credential identity lookup to the
// one fact this domain needs. It does not expose profile fields to webhook code.
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

// OwnerAgentAccessFromService evaluates issuance exactly as the named owner
// would, with no admin elevation.
type OwnerAgentAccessFromService struct{ access *agentaccess.Service }

func NewOwnerAgentAccess(access *agentaccess.Service) OwnerAgentAccessFromService {
	return OwnerAgentAccessFromService{access: access}
}

func (a OwnerAgentAccessFromService) CanUseOwner(ctx context.Context, ownerID, agentID string) (bool, error) {
	if a.access == nil {
		return false, errors.New("webhook: agent access is unavailable")
	}
	allowed, err := a.access.CanUseAsUser(ctx, ownerID, agentID)
	if errors.Is(err, agentaccess.ErrForbidden) || errors.Is(err, agentaccess.ErrNotFound) {
		return false, nil
	}
	return allowed, err
}
