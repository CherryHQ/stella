package docker

import "strings"

// Config configures the docker sandbox factory. All fields are populated by
// the runner glue layer — there are no user-facing knobs. The image is
// version-locked to the anna binary (via internal/config.SandboxDockerImage)
// and the path-prefix fields are auto-derived from ANNA_HOME_HOST when anna
// runs inside a container (see internal/agent/runner/sandbox_dood.go).
type Config struct {
	// Image is the container image to use. Required.
	Image string

	// ContainerPathPrefix / HostPathPrefix enable path alignment when anna
	// runs inside a container and talks to the daemon on the host (DooD).
	// Both fields are filled by applyDooDDefaults in the runner; callers
	// outside that path should leave them empty.
	ContainerPathPrefix string
	HostPathPrefix      string
}

// TranslateToDaemonPath rewrites an anna-view absolute path into the path the
// daemon will use as a bind-mount source. When prefix translation is not
// configured, the input is returned unchanged.
func (c Config) TranslateToDaemonPath(path string) string {
	if c.ContainerPathPrefix == "" || c.HostPathPrefix == "" {
		return path
	}
	if path == c.ContainerPathPrefix {
		return c.HostPathPrefix
	}
	prefix := c.ContainerPathPrefix
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if after, ok := strings.CutPrefix(path, prefix); ok {
		return c.HostPathPrefix + "/" + after
	}
	return path
}
