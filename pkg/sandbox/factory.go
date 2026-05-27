package sandbox

import "context"

// Factory creates sessions from policies.
type Factory interface {
	CreateSession(ctx context.Context, policy Policy) (Session, error)
	Supported(policy Policy) error
	Name() string
	Available() bool
}
