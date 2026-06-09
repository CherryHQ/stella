package docker

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// containerFilesystemMarkers are the well-known files created by container
// runtimes inside the container rootfs. Conservative list — we only consult
// these to infer that stella itself is containerized.
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

// lookupStellaHomeHost reads the STELLA_HOME_HOST env. Overridden in tests.
var lookupStellaHomeHost = func() string {
	return strings.TrimSpace(os.Getenv("STELLA_HOME_HOST"))
}

// lookupStellaHomeVolume reads the STELLA_HOME_VOLUME env. Overridden in tests.
var lookupStellaHomeVolume = func() string {
	return strings.TrimSpace(os.Getenv("STELLA_HOME_VOLUME"))
}

// applyDooDDefaults augments cfg with path-translation defaults derived from
// STELLA_HOME_HOST or STELLA_HOME_VOLUME when stella runs inside a container.
//
// Precedence:
//   - Explicit cfg.ContainerPathPrefix / cfg.HostPathPrefix always win (bind mode).
//   - In-container + STELLA_HOME_HOST set → bind-mount DooD mode; fills both
//     prefixes from env + stellaHome. STELLA_HOME_VOLUME is ignored with a warning.
//   - In-container + STELLA_HOME_VOLUME set (no STELLA_HOME_HOST) → volume mode;
//     fills cfg.StellaHomeVolume. Sandbox sessions use volume subpath mounts.
//   - In-container + neither set → error.
//   - Not in a container + either env set → warn and ignore.
func applyDooDDefaults(cfg Config, stellaHome string) (Config, error) {
	if cfg.ContainerPathPrefix != "" || cfg.HostPathPrefix != "" {
		return cfg, nil
	}
	if cfg.StellaHomeVolume == "" {
		cfg.StellaHomeVolume = lookupStellaHomeVolume()
	}
	hostPath := lookupStellaHomeHost()
	inContainer := runningInContainer()

	switch {
	case inContainer && hostPath != "":
		if cfg.StellaHomeVolume != "" {
			slog.Warn("docker backend: both STELLA_HOME_HOST and STELLA_HOME_VOLUME are set; using STELLA_HOME_HOST (bind-mount mode)",
				"component", "runner_sandbox",
			)
			cfg.StellaHomeVolume = ""
		}
		cfg.ContainerPathPrefix = stellaHome
		cfg.HostPathPrefix = hostPath
		slog.Info("docker backend: applying DooD path translation from STELLA_HOME_HOST",
			"component", "runner_sandbox",
			"container_path_prefix", cfg.ContainerPathPrefix,
			"host_path_prefix", cfg.HostPathPrefix,
		)
	case inContainer && hostPath == "" && cfg.StellaHomeVolume != "":
		slog.Info("docker backend: volume mode — STELLA_HOME is backed by a named Docker volume",
			"component", "runner_sandbox",
			"volume", cfg.StellaHomeVolume,
		)
	case inContainer && hostPath == "" && cfg.StellaHomeVolume == "":
		return cfg, fmt.Errorf(
			"docker backend: stella is running inside a container but neither STELLA_HOME_HOST nor STELLA_HOME_VOLUME is set; "+
				"set STELLA_HOME_HOST to the host-side path of STELLA_HOME (%q) for bind-mount mode, "+
				"or set STELLA_HOME_VOLUME to the Docker volume name backing STELLA_HOME for volume mode",
			stellaHome,
		)
	case !inContainer && hostPath != "":
		slog.Warn("docker backend: STELLA_HOME_HOST is set but stella is not running in a container; ignoring",
			"component", "runner_sandbox",
			"stella_home_host", hostPath,
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
