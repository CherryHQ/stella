package docker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

const dockerSandboxModeEnv = "STELLA_DOCKER_SANDBOX_MODE"

// defaultSandboxServerPort is stellad's default admin/API port (see cmd/stellad
// gateway). Auto-detected sandbox URLs target it; override the whole URL with
// STELLA_SANDBOX_SERVER_URL when stellad listens elsewhere.
const defaultSandboxServerPort = 25678

// lookupHostname reports the current hostname, which Docker defaults to the
// short container ID. Overridden in tests.
var lookupHostname = os.Hostname

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

// lookupSandboxNetwork reads the STELLA_SANDBOX_NETWORK env. Overridden in tests.
var lookupSandboxNetwork = func() string {
	return strings.TrimSpace(os.Getenv("STELLA_SANDBOX_NETWORK"))
}

// lookupSandboxServerURL reads the STELLA_SANDBOX_SERVER_URL env. Overridden in tests.
var lookupSandboxServerURL = func() string {
	return strings.TrimSpace(os.Getenv("STELLA_SANDBOX_SERVER_URL"))
}

// identifySelf resolves stellad's daemon-visible container identity. DooD owner
// labels depend on this identity, so unlike reachability detection it fails
// closed when the daemon cannot identify the current container.
func identifySelf(ctx context.Context) (*dockerclient.SelfContainer, error) {
	host, err := lookupHostname()
	if err != nil {
		return nil, fmt.Errorf("docker backend: determine container hostname: %w", err)
	}
	client, err := getSharedClient()
	if err != nil {
		return nil, fmt.Errorf("docker backend: create client to identify own container: %w", err)
	}
	return identifySelfWithClient(ctx, client, host)
}

func identifySelfWithClient(ctx context.Context, client *dockerclient.Client, host string) (*dockerclient.SelfContainer, error) {
	self, err := client.InspectSelf(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("docker backend: inspect own container %q: %w", host, err)
	}
	if self == nil || self.ID == "" {
		return nil, fmt.Errorf("docker backend: could not identify own container; docker bind and volume modes require stellad to run in a daemon-visible container")
	}
	return self, nil
}

// applyReachability merges a detected self-container into cfg, filling only the
// fields env did not already set. Pure, so the selection logic is tested without
// a daemon. Selection: an explicit SandboxNetwork wins (URL host taken from that
// same network so the two never disagree); otherwise the sole/compose-default
// user network; otherwise the default bridge by IP as a last resort.
func applyReachability(cfg Config, self *dockerclient.SelfContainer) Config {
	if self == nil {
		return cfg
	}

	network, host := "", ""
	switch {
	case cfg.SandboxNetwork != "":
		// Honor the operator's network; only fabricate a URL if stellad is
		// actually on it (else the sandbox would join a network stellad can't be
		// reached on — a misconfiguration we must not paper over).
		network = cfg.SandboxNetwork
		if on := findNetwork(self.Networks, cfg.SandboxNetwork); on != nil {
			host = self.Name // user-defined network: reach by DNS name (stable)
		} else if cfg.ServerURL == "" {
			slog.Warn("docker backend: STELLA_SANDBOX_NETWORK is not a network stellad is on; set STELLA_SANDBOX_SERVER_URL explicitly",
				"component", "runner_sandbox", "network", cfg.SandboxNetwork)
		}
	case len(self.Networks) > 0:
		chosen := pickUserNetwork(self.Networks)
		if len(self.Networks) > 1 {
			slog.Warn("docker backend: stellad is on multiple networks; using one for sandboxes — set STELLA_SANDBOX_NETWORK to override",
				"component", "runner_sandbox", "network", chosen.Name)
		}
		network, host = chosen.Name, self.Name
	case self.BridgeIP != "":
		// Bridge-only: keep the sandbox on the default bridge (network stays
		// empty) and reach stellad by its bridge IP — the bridge has no DNS.
		host = self.BridgeIP
	}

	if cfg.SandboxNetwork == "" {
		cfg.SandboxNetwork = network
	}
	if cfg.ServerURL == "" && host != "" {
		cfg.ServerURL = fmt.Sprintf("http://%s:%d", host, defaultSandboxServerPort)
	}
	slog.Info("docker backend: auto-detected sandbox reachability",
		"component", "runner_sandbox",
		"network", cfg.SandboxNetwork,
		"server_url", cfg.ServerURL,
	)
	return cfg
}

// pickUserNetwork prefers the compose project default ("*_default") so sandboxes
// land on the app network rather than an isolated/internal one; Networks is
// already sorted, so the first entry is the deterministic fallback.
func pickUserNetwork(nets []dockerclient.SelfNetwork) dockerclient.SelfNetwork {
	for _, n := range nets {
		if strings.HasSuffix(n.Name, "_default") {
			return n
		}
	}
	return nets[0]
}

func findNetwork(nets []dockerclient.SelfNetwork, name string) *dockerclient.SelfNetwork {
	for i := range nets {
		if nets[i].Name == name {
			return &nets[i]
		}
	}
	return nil
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
	if cfg.SandboxNetwork == "" {
		cfg.SandboxNetwork = lookupSandboxNetwork()
	}
	if cfg.ServerURL == "" {
		cfg.ServerURL = lookupSandboxServerURL()
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
