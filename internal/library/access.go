package library

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
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
			return Owner{}, libraryAgentAccessError(err)
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
) (LibraryFile, error) {
	file, err := s.Get(ctx, id)
	if err != nil {
		return LibraryFile{}, err
	}
	owner, err := s.ResolveManageOwner(ctx, authority, file.Owner.Scope, file.Owner.AgentID)
	switch {
	case err == nil:
	case errors.Is(err, ErrServiceUnavailable):
		return LibraryFile{}, err
	default:
		return LibraryFile{}, ErrNotFound
	}
	if owner != file.Owner {
		return LibraryFile{}, ErrNotFound
	}
	return file, nil
}

// ListManaged returns files owned by the exact authorized scope tuple together
// with its authoritative logical quota. Callers fetch limit+1 rows to determine
// whether another page exists. Personal scopes intentionally share one quota.
func (s *Service) ListManaged(
	ctx context.Context,
	authority authz.Authority,
	scope Scope,
	agentID string,
	query string,
	limit int32,
	cursor *ListCursor,
) ([]LibraryFile, Quota, error) {
	owner, err := s.ResolveManageOwner(ctx, authority, scope, agentID)
	if err != nil {
		return nil, Quota{}, err
	}
	if s == nil || s.q == nil {
		return nil, Quota{}, ErrServiceUnavailable
	}
	if limit < 1 {
		return nil, Quota{}, fmt.Errorf("%w: list limit must be positive", ErrInvalidFile)
	}

	params := sqlc.ListManagedLibraryFilesParams{
		Scope:   string(owner.Scope),
		UserID:  nullableText(owner.UserID),
		AgentID: nullableText(owner.AgentID),
		Query:   strings.TrimSpace(query),
		Limit:   limit,
	}
	if cursor != nil {
		if cursor.CreatedAt.IsZero() || cursor.ID == "" {
			return nil, Quota{}, fmt.Errorf("%w: incomplete list cursor", ErrInvalidFile)
		}
		params.CursorCreatedAt = pgtype.Timestamptz{Time: cursor.CreatedAt.UTC(), Valid: true}
		params.CursorID = nullableText(cursor.ID)
	}

	rows, err := s.q.ListManagedLibraryFiles(ctx, params)
	if err != nil {
		return nil, Quota{}, fmt.Errorf("list managed Library files: %w", err)
	}
	files := make([]LibraryFile, len(rows))
	for i, row := range rows {
		files[i] = fileFromListRow(row)
	}
	quota, err := quotaForOwner(ctx, s.q, owner)
	if err != nil {
		return nil, Quota{}, err
	}
	return files, quota, nil
}

func libraryAgentAccessError(err error) error {
	switch {
	case errors.Is(err, agentaccess.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, agentaccess.ErrForbidden):
		return ErrForbidden
	default:
		return fmt.Errorf("%w: agent access: %w", ErrServiceUnavailable, err)
	}
}
