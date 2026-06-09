package docker

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// containerFilesystemMarkers are the well-known files created by container
// runtimes inside the container rootfs.
var containerFilesystemMarkers = []string{
	"/.dockerenv",        // Docker / Moby
	"/run/.containerenv", // Podman
}

// runningInContainer reports whether stella is executing inside a container.
// Overridden in tests.
var runningInContainer = func() bool {
	for _, m := range containerFilesystemMarkers {
		if _, err := os.Stat(m); err == nil {
			return true
		}
	}
	return false
}

// lookupStellaHomeVolume reads the STELLA_HOME_VOLUME env. Overridden in tests.
var lookupStellaHomeVolume = func() string {
	return strings.TrimSpace(os.Getenv("STELLA_HOME_VOLUME"))
}

// applyVolumeDefaults populates cfg.StellaHomeVolume from STELLA_HOME_VOLUME
// when stella runs inside a container.
//
// When stellad runs inside a Docker container and uses the docker sandbox
// backend, the host daemon cannot see bind-mount sources that live inside the
// container. A named Docker volume must back STELLA_HOME so sandbox sessions
// can access workspace and tool dirs via volume subpath mounts instead.
//
// Precedence:
//   - Explicit cfg.StellaHomeVolume always wins.
//   - In-container + STELLA_HOME_VOLUME set → fills cfg.StellaHomeVolume.
//   - In-container + STELLA_HOME_VOLUME unset → error.
//   - Not in a container + STELLA_HOME_VOLUME set → warn and ignore.
func applyVolumeDefaults(cfg Config, stellaHome string) (Config, error) {
	if cfg.StellaHomeVolume == "" {
		cfg.StellaHomeVolume = lookupStellaHomeVolume()
	}
	inContainer := runningInContainer()

	switch {
	case inContainer && cfg.StellaHomeVolume != "":
		slog.Info("docker backend: volume mode — STELLA_HOME is backed by a named Docker volume",
			"component", "runner_sandbox",
			"volume", cfg.StellaHomeVolume,
		)
	case inContainer && cfg.StellaHomeVolume == "":
		return cfg, fmt.Errorf(
			"docker backend: stella is running inside a container but STELLA_HOME_VOLUME is not set; "+
				"set STELLA_HOME_VOLUME to the Docker volume name backing STELLA_HOME (%q)",
			stellaHome,
		)
	case !inContainer && cfg.StellaHomeVolume != "":
		slog.Warn("docker backend: STELLA_HOME_VOLUME is set but stella is not running in a container; ignoring",
			"component", "runner_sandbox",
			"stella_home_volume", cfg.StellaHomeVolume,
		)
		cfg.StellaHomeVolume = ""
	}
	return cfg, nil
}
