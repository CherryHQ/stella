package docker

import (
	"context"
	"fmt"
	"sync"

	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

const (
	builtinBundleRevisionLabel = "org.cherryhq.stella.builtin-bundle-revision"
	fsHelperRevisionLabel      = "org.cherryhq.stella.fs-helper-revision"
)

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

	if err := client.EnsureImageReady(ctx, cfg.Docker.Image, "preflight"); err != nil {
		return fmt.Errorf("docker preflight: %w", err)
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

	// Independent of the bundle check: the stella-fs helper baked into the image
	// must be the exact same build as the running stellad, or the mediated file
	// boundary could speak a mismatched wire protocol. A missing label reads as
	// an empty revision and fails closed against a non-empty expectation.
	if expected := cfg.Docker.ExpectedHelperRevision; expected != "" {
		actual, err := client.ImageLabel(ctx, cfg.Docker.Image, fsHelperRevisionLabel)
		if err != nil {
			return fmt.Errorf("docker preflight: inspect filesystem helper revision: %w", err)
		}
		if actual != expected {
			return fmt.Errorf("docker preflight: filesystem helper revision mismatch (expected %s, image has %s); run `mise run sandbox:docker:build` for the local image or rebuild your custom sandbox image from this Stella revision", expected, actual)
		}
	}

	return nil
}
