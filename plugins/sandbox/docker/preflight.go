package docker

import (
	"context"
	"fmt"
	"sync"

	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

const builtinBundleRevisionLabel = "org.cherryhq.stella.builtin-bundle-revision"

// ImageUnavailableError identifies image preparation failures so callers can
// attach image-specific recovery without mislabeling runtime or daemon errors.
type ImageUnavailableError struct {
	Err error
}

func (e *ImageUnavailableError) Error() string { return e.Err.Error() }
func (e *ImageUnavailableError) Unwrap() error { return e.Err }

// PreflightConfig configures a Preflight check.
type PreflightConfig struct {
	StellaHome string
	Docker     Config
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

// Preflight checks daemon reachability and image availability. A missing image
// is pulled automatically; the caller is expected to have built or published
// the image ahead of time for dev builds (the pull will fail with a registry
// error in that case).
func Preflight(ctx context.Context, cfg PreflightConfig) error {
	return preflightWithClient(ctx, cfg, nil)
}

// preflightWithClient is the testable variant that accepts an optional client override.
func preflightWithClient(ctx context.Context, cfg PreflightConfig, client *dockerclient.Client) error {
	if cfg.Docker.Image == "" {
		return fmt.Errorf("docker preflight: Image is required")
	}

	var err error
	if client == nil {
		client, err = getSharedClient()
		if err != nil {
			return fmt.Errorf("docker preflight: %w", err)
		}
	}

	if _, err := client.Version(ctx); err != nil {
		return fmt.Errorf("docker preflight: daemon not reachable: %w", err)
	}
	security, err := client.Security(ctx)
	if err != nil {
		return fmt.Errorf("docker preflight: inspect daemon security: %w", err)
	}
	if security.Rootless && security.CgroupDriver == "none" {
		return fmt.Errorf("docker preflight: rootless daemon has no cgroup driver; Stella cannot enforce sandbox CPU, memory, or PID limits")
	}
	if runtime := cfg.Docker.Runtime; runtime != "" {
		available, err := client.RuntimeAvailable(ctx, runtime)
		if err != nil {
			return fmt.Errorf("docker preflight: inspect runtime %q: %w", runtime, err)
		}
		if !available {
			return fmt.Errorf("docker preflight: runtime %q from %s is not registered with the Docker daemon", runtime, dockerRuntimeEnv)
		}
	}

	if err := client.EnsureImageReady(ctx, cfg.Docker.Image, "preflight"); err != nil {
		return fmt.Errorf("docker preflight: %w", &ImageUnavailableError{Err: err})
	}
	if expected := cfg.Docker.ExpectedBundleRevision; expected != "" {
		actual, err := client.ImageLabel(ctx, cfg.Docker.Image, builtinBundleRevisionLabel)
		if err != nil {
			return fmt.Errorf("docker preflight: inspect builtin bundle revision: %w", err)
		}
		if actual != expected {
			return fmt.Errorf("docker preflight: builtin bundle revision mismatch (expected %s, image has %s); run `mise run sandbox:docker:build` for the local image or rebuild your custom sandbox image from this Stella revision", expected, actual)
		}
	}

	return nil
}
