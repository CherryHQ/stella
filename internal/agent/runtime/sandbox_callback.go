package runtime

import (
	"context"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// SandboxSessionCallback is invoked with a live runner-owned sandbox immediately
// before Runtime closes that session's runner. The callback must not retain the
// handle after returning; CloseSessionWithSandbox closes it next.
type SandboxSessionCallback func(sandbox.Session) error

// CloseSessionWithSandbox closes the runner for one session, first exposing the
// live sandbox to cb when the runner has one. The runner is closed after cb
// returns even when cb reports an error.
func (rt *Runtime) CloseSessionWithSandbox(_ context.Context, sessionID string, cb SandboxSessionCallback) error {
	return rt.cache.closeWithSandbox(sessionID, cb)
}
