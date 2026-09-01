//go:build !windows

package docker

import (
	"fmt"
	"os"
)

// dockerProcessUser renders the ownership model exposed by the daemon. Rootless
// bind mounts expose the daemon user's host files as container UID 0. Named
// volumes are different: stellad and the sandbox share the daemon's UID mapping,
// so the sandbox must keep stellad's process UID to access its 0700 trees.
func dockerProcessUser(rootless bool, mode DockerSandboxMode) string {
	if rootless && mode != DockerSandboxModeVolume {
		return "0:0"
	}
	return fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
}
