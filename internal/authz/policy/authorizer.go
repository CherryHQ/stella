package policy

import (
	"context"
	"errors"

	"github.com/CherryHQ/stella/internal/authz"
)

// ErrInvalidAuthority is returned by Begin when the supplied Authority is not
// well-formed. It denies the use case before any decision can be made.
var ErrInvalidAuthority = errors.New("authz/policy: invalid authority")

// Authorizer evaluates the fixed built-in rule set. It has no mutable state and
// never consults persistence: domain PEPs provide the durable facts each rule
// needs in their typed requests.
type Authorizer struct{}

// New builds the static built-in Authorizer.
func New() *Authorizer { return &Authorizer{} }

// Begin validates Authority and binds it to an immutable static Evaluation.
func (*Authorizer) Begin(_ context.Context, authority authz.Authority) (authz.Evaluation, error) {
	if !authority.Valid() {
		return nil, ErrInvalidAuthority
	}
	return &evaluation{authority: authority, snap: staticSnapshot}, nil
}
