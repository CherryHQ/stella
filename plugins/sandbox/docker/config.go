package docker

import "strings"

// Config configures the docker sandbox factory.
type Config struct {
	// Image is the container image to use. Required.
	Image string

	// StellaHome is the host-view stella home directory. Used for orphan
	// cleanup scoping, preflight checks, DooD path translation, and
	// resolving user tool binaries from the plugins manifest.
	StellaHome string

	// ContainerPathPrefix / HostPathPrefix enable path alignment when stella
	// runs inside a container and talks to the daemon on the host (DooD).
	// Normally auto-derived from STELLA_HOME_HOST by NewFactory; only set
	// explicitly in tests or when overriding the default detection.
	ContainerPathPrefix string
	HostPathPrefix      string

	// UserToolBinaries are manifest-declared, user-configured CLIs that are not
	// baked into the versioned sandbox image. They are installed in a Linux
	// helper container and exposed to sessions through a Docker-managed tool
	// cache, never through host $STELLA_HOME/bin.
	// Normally auto-resolved by NewFactory from $STELLA_HOME/plugins.yaml.
	UserToolBinaries []ToolBinary
}

// TranslateToDaemonPath rewrites an stella-view absolute path into the path the
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
