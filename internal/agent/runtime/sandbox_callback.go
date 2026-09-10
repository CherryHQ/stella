package runtime

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// SandboxResultCallback checks the live sandbox before runtime completes the
// turn. It must use the supplied execution context and not retain the handle.
type SandboxResultCallback func(context.Context, sandbox.Session) error

func WithSandboxResult(cb SandboxResultCallback) Option {
	return func(o *chatOptions) { o.sandboxResult = cb }
}

func commitSandboxResult(ctx context.Context, runner Runner, cb SandboxResultCallback) error {
	if sr, ok := runner.(interface{ SandboxSession() sandbox.Session }); ok {
		if sess := sr.SandboxSession(); sess != nil {
			if err := cb(ctx, sess); err != nil {
				return fmt.Errorf("commit sandbox result: %w", err)
			}
		}
	}
	return nil
}
