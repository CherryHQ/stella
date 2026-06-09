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

// lookupStellaHomeHost reads the STELLA_HOME_HOST env. Overridden in tests.
var lookupStellaHomeHost = func() string {
	return strings.TrimSpace(os.Getenv("STELLA_HOME_HOST"))
}

// lookupStellaHomeVolume reads the STELLA_HOME_VOLUME env. Overridden in tests.
var lookupStellaHomeVolume = func() string {
	return strings.TrimSpace(os.Getenv("STELLA_HOME_VOLUME"))
}

// applyDooDDefaults augments cfg when stella runs inside a container and uses
// the host Docker daemon for sandbox containers.
//
// Supported in-container docker-sandbox modes:
//   - STELLA_HOME_HOST: STELLA_HOME is a host bind mount; bind sources are
//     translated from the stella container path to the daemon-visible host path.
//   - STELLA_HOME_VOLUME: STELLA_HOME is a Docker named volume; sandbox sessions
//     use volume subpath mounts and do not need a host-visible path.
//
// STELLA_HOME_HOST and STELLA_HOME_VOLUME are mutually exclusive. When stella is
// already running on the host, both are ignored because daemon paths are already
// host paths.
func applyDooDDefaults(cfg Config, stellaHome string) (Config, error) {
	inContainer := runningInContainer()
	hostPath := lookupStellaHomeHost()
	volume := lookupStellaHomeVolume()

	if cfg.StellaHomeVolume == "" {
		cfg.StellaHomeVolume = volume
	}
	if cfg.ContainerPathPrefix == "" && cfg.HostPathPrefix == "" && hostPath != "" {
		cfg.ContainerPathPrefix = stellaHome
		cfg.HostPathPrefix = hostPath
	}

	bindConfigured := cfg.ContainerPathPrefix != "" || cfg.HostPathPrefix != ""
	if !inContainer {
		if cfg.StellaHomeVolume != "" {
			slog.Warn("docker backend: STELLA_HOME_VOLUME is set but stella is not running in a container; ignoring",
				"component", "runner_sandbox",
				"stella_home_volume", cfg.StellaHomeVolume,
			)
			cfg.StellaHomeVolume = ""
		}
		if bindConfigured {
			slog.Warn("docker backend: STELLA_HOME_HOST is set but stella is not running in a container; ignoring",
				"component", "runner_sandbox",
				"stella_home_host", cfg.HostPathPrefix,
			)
			cfg.ContainerPathPrefix = ""
			cfg.HostPathPrefix = ""
		}
		return cfg, nil
	}
	if cfg.StellaHomeVolume != "" && bindConfigured {
		return cfg, fmt.Errorf("docker backend: STELLA_HOME_HOST and STELLA_HOME_VOLUME are mutually exclusive; set only one")
	}
	if bindConfigured && (cfg.ContainerPathPrefix == "" || cfg.HostPathPrefix == "") {
		return cfg, fmt.Errorf("docker backend: container_path_prefix and host_path_prefix must be set together")
	}

	switch {
	case cfg.StellaHomeVolume != "":
		slog.Info("docker backend: volume mode — STELLA_HOME is backed by a named Docker volume",
			"component", "runner_sandbox",
			"volume", cfg.StellaHomeVolume,
		)
	case bindConfigured:
		slog.Info("docker backend: bind-mount mode — applying DooD path translation from STELLA_HOME_HOST",
			"component", "runner_sandbox",
			"container_path_prefix", cfg.ContainerPathPrefix,
			"host_path_prefix", cfg.HostPathPrefix,
		)
	default:
		return cfg, fmt.Errorf(
			"docker backend: stella is running inside a container but neither STELLA_HOME_HOST nor STELLA_HOME_VOLUME is set; "+
				"set STELLA_HOME_HOST to the host-side path of STELLA_HOME (%q) for a bind mount, "+
				"or STELLA_HOME_VOLUME to the Docker volume name backing STELLA_HOME",
			stellaHome,
		)
	}
	return cfg, nil
}
