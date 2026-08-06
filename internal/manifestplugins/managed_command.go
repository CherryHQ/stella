package manifestplugins

import (
	"context"
	"os/exec"
	"time"
)

const managedCommandWaitDelay = 5 * time.Second

// ManagedCommandContext creates a command whose cancellation terminates the
// entire process tree and whose Wait call cannot hang forever on inherited
// pipes. Callers remain responsible for setting the working directory,
// environment, and standard streams before starting the command.
func ManagedCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return managedCommandContext(ctx, name, args...)
}
