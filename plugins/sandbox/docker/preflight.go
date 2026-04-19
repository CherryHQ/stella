package docker

import (
	"context"
	"fmt"
	"sync"

	"github.com/vaayne/anna/plugins/sandbox/docker/dockerclient"
)

// PreflightConfig configures a Preflight check.
type PreflightConfig struct {
	AnnaHome string
	Docker   Config
}

// sharedClient is a package-level cached client.
// Tests bypass this by using dockerclient.NewWithPath and passing a client directly.
var (
	sharedClientOnce sync.Once
	sharedClient     *dockerclient.Client
	sharedClientErr  error
)

// getSharedClient returns the cached dockerclient.Client, initializing it once.
func getSharedClient() (*dockerclient.Client, error) {
	sharedClientOnce.Do(func() {
		sharedClient, sharedClientErr = dockerclient.New()
	})
	return sharedClient, sharedClientErr
}

// Preflight checks daemon reachability and image availability.
// If the image is missing and cfg.Docker.AllowsImplicitPull() is true, pulls it.
// If missing and pulling is not allowed, returns an error.
func Preflight(ctx context.Context, cfg PreflightConfig) error {
	return preflightWithClient(ctx, cfg, nil)
}

// preflightWithClient is the testable variant that accepts an optional client override.
func preflightWithClient(ctx context.Context, cfg PreflightConfig, client *dockerclient.Client) error {
	var err error
	if client == nil {
		client, err = getSharedClient()
		if err != nil {
			return fmt.Errorf("docker preflight: %w", err)
		}
	}

	// Check daemon reachability.
	if _, err := client.Version(ctx); err != nil {
		return fmt.Errorf("docker preflight: daemon not reachable: %w", err)
	}

	image := cfg.Docker.ImageOrDefault()

	exists, err := client.ImageExists(ctx, image)
	if err != nil {
		return fmt.Errorf("docker preflight: check image %q: %w", image, err)
	}

	if exists {
		return nil
	}

	if !cfg.Docker.AllowsImplicitPull() {
		return fmt.Errorf("docker preflight: image %q not found locally and AllowPull is false; run `docker pull %s` or set AllowPull=true", image, image)
	}

	if err := client.PullImage(ctx, image); err != nil {
		return fmt.Errorf("docker preflight: pull image %q: %w", image, err)
	}

	return nil
}
