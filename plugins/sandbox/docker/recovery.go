package docker

import (
	"context"

	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

// CaptureStartupRecovery snapshots the scoped Docker containers that existed
// before the current runtime starts. The returned check only considers that
// snapshot, so a healthy container created later by this runtime cannot keep a
// crashed predecessor's host resources pending forever.
func CaptureStartupRecovery(ctx context.Context, cfg Config) (func(context.Context) (bool, error), error) {
	if cfg.StellaHome == "" {
		return func(context.Context) (bool, error) { return true, nil }, nil
	}
	resolved, err := resolveDockerConfig(cfg, cfg.StellaHome)
	if err != nil {
		return nil, err
	}
	scope := resolved.cleanupScope(resolved.StellaHome)
	if scope == "" {
		return func(context.Context) (bool, error) { return true, nil }, nil
	}
	client, err := getSharedClient()
	if err != nil {
		return nil, err
	}
	return dockerclient.CaptureOrphanedContainers(ctx, client, scope)
}
