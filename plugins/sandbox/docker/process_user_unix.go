//go:build !windows

package docker

import (
	"fmt"
	"os"
)

// dockerProcessUser renders the ownership model exposed by the daemon. UID 0
// on a rootless daemon maps to the unprivileged daemon user on the host and is
// the owner rootless bind mounts expose inside the container.
func dockerProcessUser(rootless bool) string {
	if rootless {
		return "0:0"
	}
	return fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
}
