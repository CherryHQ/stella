package runner

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	dockerplugin "github.com/vaayne/anna/plugins/sandbox/docker"
)

// containerFilesystemMarkers are the well-known files created by container
// runtimes inside the container rootfs. Conservative list — we only consult
// these to infer that anna itself is containerized.
var containerFilesystemMarkers = []string{
	"/.dockerenv",        // Docker / Moby
	"/run/.containerenv", // Podman
}

// runningInContainer reports whether anna is executing inside a container.
// Overridden in tests.
var runningInContainer = func() bool {
	for _, m := range containerFilesystemMarkers {
		if _, err := os.Stat(m); err == nil {
			return true
		}
	}
	return false
}

// lookupAnnaHomeHost reads the ANNA_HOME_HOST env. Overridden in tests.
var lookupAnnaHomeHost = func() string {
	return strings.TrimSpace(os.Getenv("ANNA_HOME_HOST"))
}

// applyDooDDefaults augments cfg with path-translation defaults derived from
// ANNA_HOME_HOST when anna runs inside a container (Docker-outside-of-Docker).
// Precedence:
//   - Explicit cfg.ContainerPathPrefix / cfg.HostPathPrefix always win.
//   - In-container + ANNA_HOME_HOST set → fill both prefixes from env + annaHome.
//   - In-container + ANNA_HOME_HOST unset → error, since bind-mount sources
//     would otherwise be sent to the daemon using paths that don't exist on
//     the host.
//   - Not in a container + ANNA_HOME_HOST set → warn and ignore; "host path"
//     is meaningless when anna already runs on the host.
func applyDooDDefaults(cfg dockerplugin.Config, annaHome string) (dockerplugin.Config, error) {
	if cfg.ContainerPathPrefix != "" || cfg.HostPathPrefix != "" {
		return cfg, nil
	}
	hostPath := lookupAnnaHomeHost()
	inContainer := runningInContainer()

	switch {
	case inContainer && hostPath != "":
		cfg.ContainerPathPrefix = annaHome
		cfg.HostPathPrefix = hostPath
		slog.Info("docker backend: applying DooD path translation from ANNA_HOME_HOST",
			"component", "runner_sandbox",
			"container_path_prefix", cfg.ContainerPathPrefix,
			"host_path_prefix", cfg.HostPathPrefix,
		)
	case inContainer && hostPath == "":
		return cfg, fmt.Errorf(
			"docker backend: anna is running inside a container but ANNA_HOME_HOST is not set; "+
				"set ANNA_HOME_HOST to the host-side path of ANNA_HOME (%q), "+
				"or set sandbox.docker.container_path_prefix and host_path_prefix on the agent",
			annaHome,
		)
	case !inContainer && hostPath != "":
		slog.Warn("docker backend: ANNA_HOME_HOST is set but anna is not running in a container; ignoring",
			"component", "runner_sandbox",
			"anna_home_host", hostPath,
		)
	}
	return cfg, nil
}
