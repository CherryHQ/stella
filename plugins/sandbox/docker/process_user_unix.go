//go:build !windows

package docker

import (
	"fmt"
	"os"
)

// dockerProcessUser aligns bind/volume file ownership with the stellad process.
// The container still drops every capability and runs with no-new-privileges.
func dockerProcessUser() string {
	return fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
}
