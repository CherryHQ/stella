package toolctx

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

// FromContext extracts the runtime identity for native tools. Group and
// unauthenticated sessions have no user id, so domain tools must refuse them.
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

func NotFound() error { return ErrNotFound }

func Forbidden() error { return ErrForbidden }

func Unauthenticated() error { return ErrUnauthenticated }

func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

func IsForbidden(err error) bool { return errors.Is(err, ErrForbidden) }

func IsUnauthenticated(err error) bool { return errors.Is(err, ErrUnauthenticated) }
