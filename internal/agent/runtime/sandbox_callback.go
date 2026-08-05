package runtime

import (
	"context"
	"errors"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// SandboxSessionCallback is invoked with a live runner-owned sandbox immediately
// before Runtime closes that session's runner. The callback must not retain the
// handle after returning; CloseSessionWithSandbox closes it next.
type SandboxSessionCallback func(sandbox.Session) error

// FilesystemCallback receives a short-lived provider Filesystem. It cannot
// retain a runner, Session, or host path beyond the callback.
type FilesystemCallback func(sandbox.Filesystem) error

// UseFilesystem creates or reuses the exact session runner and leases it for
// one callback. The lease coexists with a chat turn but blocks ordinary reset,
// invalidation, and reaping until the callback returns; a terminal owner delete
// may still interrupt it. It never returns a handle: the Filesystem is opened
// and closed within the lease, and its close error joins the result.
func (rt *Runtime) UseFilesystem(ctx context.Context, info session.Info, cb FilesystemCallback) (err error) {
	if rt.closed.Load() {
		return errors.New("runtime is closed")
	}
	if cb == nil {
		return errors.New("filesystem callback is required")
	}
	selection, err := rt.cache.acquireFilesystemUse(ctx, info)
	if err != nil {
		return err
	}
	// Exactly one owner releases the lease: this deferred release runs on every
	// return and every panic. It only decrements the count; it never quarantines
	// the runner, so a callback panic cannot fail an otherwise healthy runner.
	defer rt.cache.releaseFilesystemUse(selection.session)

	// A context canceled between admission and the callback releases the lease
	// and returns without opening the Filesystem or invoking cb.
	if err := ctx.Err(); err != nil {
		return err
	}
	filesystem, err := rt.openLeasedFilesystem(selection)
	if err != nil {
		return err
	}
	// Close the Filesystem before releasing the lease (LIFO) and join its close
	// error. A callback panic becomes a generic error: it is the caller's fault,
	// not the runner's, so the leased runner stays healthy for its active turn.
	defer func() {
		panicked := recover()
		closeErr := filesystem.Close()
		if panicked != nil {
			err = errors.New("filesystem callback panicked")
		}
		err = errors.Join(err, closeErr)
	}()
	return cb(filesystem)
}

// openLeasedFilesystem resolves the leased runner's mediated Filesystem. Type
// assertions fail closed. A panic in the runner or its provider quarantines the
// runner for rebuild without touching the lease, which its caller releases.
func (rt *Runtime) openLeasedFilesystem(selection runnerSelection) (filesystem sandbox.Filesystem, err error) {
	defer func() {
		if recover() != nil {
			rt.cache.quarantineLeasedRunner(selection.session)
			filesystem, err = nil, errors.New("sandbox provider panicked")
		}
	}()
	owner, ok := selection.runner.(interface{ SandboxSession() sandbox.Session })
	if !ok {
		return nil, errors.New("runner lacks sandbox session capability")
	}
	sess := owner.SandboxSession()
	if sess == nil {
		return nil, errors.New("runner lacks sandbox session capability")
	}
	fsSession, ok := sess.(sandbox.FilesystemSession)
	if !ok {
		return nil, errors.New("sandbox lacks filesystem capability")
	}
	fs, err := fsSession.Filesystem()
	if err != nil {
		return nil, err
	}
	// A provider must never hand back a nil Filesystem with a nil error: closing
	// or calling it would panic. Reject it here rather than let it reach cb.
	if fs == nil {
		return nil, errors.New("sandbox returned a nil filesystem")
	}
	return fs, nil
}

// CloseSessionWithSandbox closes the runner for one session, first exposing the
// live sandbox to cb when the runner has one. The runner is closed after cb
// returns even when cb reports an error.
func (rt *Runtime) CloseSessionWithSandbox(_ context.Context, sessionID string, cb SandboxSessionCallback) error {
	return rt.cache.closeWithSandbox(sessionID, cb)
}
