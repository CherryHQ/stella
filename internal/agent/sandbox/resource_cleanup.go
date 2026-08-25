package sandbox

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/config"
	dockerclient "github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

// CleanupDurableResource reconstructs provider cleanup from the identity
// persisted before sandbox creation. Host targets are launch-gated until their
// PID/start-time identity is durable; Docker uses the deterministic resource
// name. Both cleanup paths treat verified absence as success.
func CleanupDurableResource(ctx context.Context, backend, resourceID string) error {
	if resourceID == "" {
		return fmt.Errorf("sandbox cleanup: resource identity is empty")
	}
	switch backend {
	case "process", config.SandboxBackendLocal, config.SandboxBackendNone:
		return cleanupHostProcessResource(ctx, resourceID)
	case config.SandboxBackendDocker:
		client, err := dockerclient.New()
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		return client.Stop(ctx, "stella-sandbox-"+resourceID)
	default:
		return fmt.Errorf("sandbox cleanup: unsupported backend %q", backend)
	}
}
