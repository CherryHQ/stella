package authz

import (
	"context"
	"errors"

	"github.com/CherryHQ/stella/internal/memory"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrForbidden       = errors.New("permission denied")
	ErrUnauthenticated = errors.New("authentication required - ask the user to run this from a signed-in one-on-one session")
)

// Identity is the non-spoofable runtime identity carried by context or an HTTP
// adapter. AgentScoped means access must stay inside AgentID's resource boundary.
type Identity struct {
	UserID      string
	AgentID     string
	AgentScoped bool
}

// FromContext extracts the runtime identity for built-in tools. Group and
// unauthenticated sessions have no user id, so identity-scoped tools must refuse them.
// An agent session is always confined to its own agent's resources — the same
// boundary the sandbox scoped token enforced on the HTTP path.
func FromContext(ctx context.Context) (Identity, error) {
	ident := Identity{
		UserID:  memory.UserIDFromContext(ctx),
		AgentID: memory.AgentIDFromContext(ctx),
	}
	if ident.UserID == "" {
		return Identity{}, ErrUnauthenticated
	}
	ident.AgentScoped = ident.AgentID != ""
	return ident, nil
}

// RequireUser rejects unauthenticated identities.
func (id Identity) RequireUser() error {
	if id.UserID == "" {
		return ErrUnauthenticated
	}
	return nil
}

// ResolveAgentScope confines a requested agent id to the bound agent when scoped.
func (id Identity) ResolveAgentScope(requested string) (string, error) {
	if !id.AgentScoped {
		return requested, nil
	}
	if id.AgentID == "" || (requested != "" && requested != id.AgentID) {
		return "", ErrForbidden
	}
	return id.AgentID, nil
}

// RequireAgentMatch rejects a different agent when the identity is scoped.
func (id Identity) RequireAgentMatch(agentID string) error {
	if id.AgentScoped && (id.AgentID == "" || agentID != id.AgentID) {
		return ErrForbidden
	}
	return nil
}

func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

func IsForbidden(err error) bool { return errors.Is(err, ErrForbidden) }
