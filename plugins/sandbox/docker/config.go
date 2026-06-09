package docker

// Config configures the docker sandbox factory.
type Config struct {
	// Image is the container image to use. Required.
	Image string

	// StellaHome is the container-view stella home directory. Used for orphan
	// cleanup scoping, preflight checks, and resolving user tool binaries from
	// the plugins manifest.
	StellaHome string

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
