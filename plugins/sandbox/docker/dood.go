package docker

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

const dockerSandboxModeEnv = "STELLA_DOCKER_SANDBOX_MODE"

// lookupDockerSandboxMode reads STELLA_DOCKER_SANDBOX_MODE. Overridden in tests.
var lookupDockerSandboxMode = func() string {
	return strings.TrimSpace(os.Getenv(dockerSandboxModeEnv))
}

// lookupStellaHomeHost reads the STELLA_HOME_HOST env. Overridden in tests.
var lookupStellaHomeHost = func() string {
	return strings.TrimSpace(os.Getenv("STELLA_HOME_HOST"))
}

// lookupStellaHomeVolume reads the STELLA_HOME_VOLUME env. Overridden in tests.
var lookupStellaHomeVolume = func() string {
	return strings.TrimSpace(os.Getenv("STELLA_HOME_VOLUME"))
}

// applyDockerMode fills and validates the explicit docker sandbox runtime mode.
//
// STELLA_DOCKER_SANDBOX_MODE is required when NewFactory receives StellaHome:
//   - host: stellad runs on the host; stella-process paths are daemon-visible.
//   - bind: stellad runs in a container; STELLA_HOME_HOST is the host-side path.
//   - volume: stellad runs in a container; STELLA_HOME_VOLUME is the volume name.
//
// This intentionally does not inspect /.dockerenv or other runtime markers.
// The caller describes the deployment; the backend validates that the mode has
// exactly the env needed for that mode and no conflicting mode env.
func applyDockerMode(cfg Config, stellaHome string) (Config, error) {
	if cfg.RuntimeMode == "" {
		cfg.RuntimeMode = DockerSandboxMode(lookupDockerSandboxMode())
	}
	if cfg.StellaHomeVolume == "" {
		cfg.StellaHomeVolume = lookupStellaHomeVolume()
	}
	if cfg.ContainerPathPrefix == "" && cfg.HostPathPrefix == "" {
		if hostPath := lookupStellaHomeHost(); hostPath != "" {
			cfg.ContainerPathPrefix = stellaHome
			cfg.HostPathPrefix = hostPath
		}
	}

	bindConfigured := cfg.ContainerPathPrefix != "" || cfg.HostPathPrefix != ""
	switch cfg.RuntimeMode {
	case DockerSandboxModeHost:
		if bindConfigured || cfg.StellaHomeVolume != "" {
			return cfg, fmt.Errorf("docker backend: mode %q must not set STELLA_HOME_HOST or STELLA_HOME_VOLUME", cfg.RuntimeMode)
		}
		slog.Info("docker backend: host mode — STELLA_HOME paths are daemon-visible",
			"component", "runner_sandbox",
		)
	case DockerSandboxModeBind:
		if cfg.StellaHomeVolume != "" {
			return cfg, fmt.Errorf("docker backend: mode %q must not set STELLA_HOME_VOLUME", cfg.RuntimeMode)
		}
		if !bindConfigured || cfg.ContainerPathPrefix == "" || cfg.HostPathPrefix == "" {
			return cfg, fmt.Errorf("docker backend: mode %q requires STELLA_HOME_HOST to point at the host-side path of STELLA_HOME (%q)", cfg.RuntimeMode, stellaHome)
		}
		slog.Info("docker backend: bind mode — translating STELLA_HOME paths for Docker-outside-of-Docker",
			"component", "runner_sandbox",
			"container_path_prefix", cfg.ContainerPathPrefix,
			"host_path_prefix", cfg.HostPathPrefix,
		)
	case DockerSandboxModeVolume:
		if bindConfigured {
			return cfg, fmt.Errorf("docker backend: mode %q must not set STELLA_HOME_HOST", cfg.RuntimeMode)
		}
		if cfg.StellaHomeVolume == "" {
			return cfg, fmt.Errorf("docker backend: mode %q requires STELLA_HOME_VOLUME to name the Docker volume backing STELLA_HOME (%q)", cfg.RuntimeMode, stellaHome)
		}
		slog.Info("docker backend: volume mode — STELLA_HOME is backed by a named Docker volume",
			"component", "runner_sandbox",
			"volume", cfg.StellaHomeVolume,
		)
	case "":
		return cfg, fmt.Errorf("docker backend: %s is required and must be one of %q, %q, or %q", dockerSandboxModeEnv, DockerSandboxModeHost, DockerSandboxModeBind, DockerSandboxModeVolume)
	default:
		return cfg, fmt.Errorf("docker backend: invalid %s=%q; expected %q, %q, or %q", dockerSandboxModeEnv, cfg.RuntimeMode, DockerSandboxModeHost, DockerSandboxModeBind, DockerSandboxModeVolume)
	}
	return cfg, nil
}
