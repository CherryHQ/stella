package docker

import "strings"

// Config configures the docker sandbox factory.
type Config struct {
	// Image is the container image to use. Required.
	Image string

	// StellaHome is the stella-process-view home directory. When stellad runs
	// inside a container this is the in-container path; when it runs on the host
	// this is the host path. Used for orphan cleanup scoping, preflight checks,
	// DooD path translation, and resolving user tool binaries from the plugins
	// manifest.
	StellaHome string

	// ContainerPathPrefix / HostPathPrefix enable bind-mount path alignment when
	// stella runs inside a container and talks to the daemon on the host (DooD).
	// Normally auto-derived from STELLA_HOME_HOST by NewFactory; only set
	// explicitly in tests or when overriding the default detection.
	ContainerPathPrefix string
	HostPathPrefix      string

	// StellaHomeVolume is the Docker named volume that backs STELLA_HOME.
	// Set this (via STELLA_HOME_VOLUME env) when stella runs inside a container
	// whose STELLA_HOME is a Docker named volume. Sandbox sessions then use
	// volume subpath mounts (requires Docker Engine 25+) instead of bind mounts,
	// so the host daemon never needs a host-filesystem-visible path.
	// Normally auto-derived from STELLA_HOME_VOLUME by NewFactory.
	StellaHomeVolume string

	// UserToolBinaries are manifest-declared, user-configured CLIs that are not
	// baked into the versioned sandbox image. They are installed in a Linux
	// helper container and exposed to sessions through a Docker-managed tool
	// cache, never through host $STELLA_HOME/bin.
	// Normally auto-resolved by NewFactory from $STELLA_HOME/plugins.yaml.
	UserToolBinaries []ToolBinary
}

// TranslateToDaemonPath rewrites a stella-process-view absolute path into the
// path the daemon will use as a bind-mount source. When prefix translation is
// not configured, the input is returned unchanged.
func (c Config) TranslateToDaemonPath(path string) string {
	translated, ok := c.daemonPath(path)
	if !ok {
		return path
	}
	return translated
}

// daemonPath rewrites path and reports whether it is safe to hand to the Docker
// daemon as a bind-mount source. In DooD bind mode, paths outside the configured
// container prefix are not daemon-visible and must be skipped/fail closed.
func (c Config) daemonPath(path string) (string, bool) {
	if c.ContainerPathPrefix == "" || c.HostPathPrefix == "" {
		return path, true
	}
	if path == c.ContainerPathPrefix {
		return c.HostPathPrefix, true
	}
	prefix := c.ContainerPathPrefix
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if after, ok := strings.CutPrefix(path, prefix); ok {
		return c.HostPathPrefix + "/" + after, true
	}
	return "", false
}

func (c Config) cleanupScope(stellaHome string) string {
	if c.StellaHomeVolume != "" {
		return "volume:" + c.StellaHomeVolume
	}
	return c.TranslateToDaemonPath(stellaHome)
}
