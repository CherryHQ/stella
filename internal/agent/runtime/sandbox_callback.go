package runtime

import (
	"context"
	"errors"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// SandboxSessionCallback is invoked with a live runner-owned sandbox immediately
// before Runtime closes that session's runner. The callback must not retain the
// handle after returning; CloseSessionWithSandbox closes it next.
type SandboxSessionCallback func(sandbox.Session) error

// CloseSessionWithSandbox closes the runner for one session, first exposing the
// live sandbox to cb when the runner has one. The runner is closed after cb
// returns even when cb reports an error.
func (rt *Runtime) CloseSessionWithSandbox(ctx context.Context, sessionID string, cb SandboxSessionCallback) error {
	err := rt.cache.closeWithSandbox(sessionID, cb)
	return errors.Join(err, rt.closeSessionOwner(ctx, sessionID))
}
