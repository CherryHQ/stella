package channel

import (
	"errors"

	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// publicAccessError keeps the established transport text and internal
// errors.Is behavior while exposing the narrow category that channel plugins
// are allowed to consume.
type publicAccessError struct {
	err      error
	category error
}

func (e publicAccessError) Error() string { return e.err.Error() }
func (e publicAccessError) Unwrap() error { return e.err }
func (e publicAccessError) Is(target error) bool {
	return target == e.category || errors.Is(e.err, target)
}

func mapPublicAccessError(err error) error {
	switch {
	case errors.Is(err, ErrAgentAccessDenied):
		return publicAccessError{err: err, category: pkgchannel.ErrAgentAccessDenied}
	case errors.Is(err, agentaccess.ErrForbidden):
		return publicAccessError{err: err, category: pkgchannel.ErrAgentAccessForbidden}
	default:
		return err
	}
}
