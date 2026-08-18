//go:build !windows

package docker

import (
	"fmt"
	"os"
)

// rootfulDockerProcessUser aligns bind/volume ownership with stellad. Rootless
// Docker instead maps the daemon user to container UID 0; selecting the host
// numeric UID there makes daemon-user-owned bind mounts unwritable.
func rootfulDockerProcessUser() string {
	return fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
}
