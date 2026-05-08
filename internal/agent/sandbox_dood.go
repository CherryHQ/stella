package agent

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	dockerplugin "github.com/CherryHQ/stella/plugins/sandbox/docker"
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

// applyDooDDefaults augments cfg with path-translation defaults derived from
// STELLA_HOME_HOST when stella runs inside a container (Docker-outside-of-Docker).
// Precedence:
//   - Explicit cfg.ContainerPathPrefix / cfg.HostPathPrefix always win.
//   - In-container + STELLA_HOME_HOST set → fill both prefixes from env + stellaHome.
//   - In-container + STELLA_HOME_HOST unset → error, since bind-mount sources
//     would otherwise be sent to the daemon using paths that don't exist on
//     the host.
//   - Not in a container + STELLA_HOME_HOST set → warn and ignore; "host path"
//     is meaningless when stella already runs on the host.
func applyDooDDefaults(cfg dockerplugin.Config, stellaHome string) (dockerplugin.Config, error) {
	if cfg.ContainerPathPrefix != "" || cfg.HostPathPrefix != "" {
		return cfg, nil
	}
	hostPath := lookupStellaHomeHost()
	inContainer := runningInContainer()

	switch {
	case inContainer && hostPath != "":
		cfg.ContainerPathPrefix = stellaHome
		cfg.HostPathPrefix = hostPath
		slog.Info("docker backend: applying DooD path translation from STELLA_HOME_HOST",
			"component", "runner_sandbox",
			"container_path_prefix", cfg.ContainerPathPrefix,
			"host_path_prefix", cfg.HostPathPrefix,
		)
	case inContainer && hostPath == "":
		return cfg, fmt.Errorf(
			"docker backend: stella is running inside a container but STELLA_HOME_HOST is not set; "+
				"set STELLA_HOME_HOST to the host-side path of STELLA_HOME (%q), "+
				"or set sandbox.docker.container_path_prefix and host_path_prefix on the agent",
			stellaHome,
		)
	case !inContainer && hostPath != "":
		slog.Warn("docker backend: STELLA_HOME_HOST is set but stella is not running in a container; ignoring",
			"component", "runner_sandbox",
			"stella_home_host", hostPath,
		)
	}
	return cfg, nil
}
