package dockerclient

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	mobyclient "github.com/moby/moby/client"
)

// NetworkMode controls the container's network access.
type NetworkMode string

const (
	NetworkDisabled NetworkMode = "disabled"
	NetworkAllowAll NetworkMode = "allow_all"
)

// Mount represents a bind-mount from host to container. Source is interpreted
// by the daemon, so when anna runs inside a container it must already be
// translated to a daemon-visible path before reaching this struct.
type Mount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// CreateOptions configures a new sandbox container.
type CreateOptions struct {
	Image          string
	WorkspaceHost  string      // absolute host path (daemon-side)
	WorkspaceMount string      // absolute in-container path (e.g. "/home/anna/workspace")
	ReadOnlyMounts []Mount     // host -> container, read-only
	NetworkMode    NetworkMode // disabled | allow_all
	Env            map[string]string
	Labels         map[string]string // must include LabelSessionID + LabelAnnaHome + LabelCreatedAt
	Name           string            // optional; caller builds "anna-sandbox-<session-id>"
}

// CreateAndStart creates a container with an always-up sentinel entrypoint
// (`sh -c 'tail -f /dev/null'`), starts it, and returns the container ID.
// If the image is not present locally it is pulled automatically.
func (c *Client) CreateAndStart(ctx context.Context, opts CreateOptions) (string, error) {
	exists, err := c.ImageExists(ctx, opts.Image)
	if err != nil {
		return "", fmt.Errorf("dockerclient: image check %s: %w", opts.Image, err)
	}
	if !exists {
		slog.Info("dockerclient: image not found locally, pulling", "image", opts.Image)
		if err := c.PullImage(ctx, opts.Image); err != nil {
			return "", err
		}
	}

	createOpts := buildContainerCreateOptions(opts)

	created, err := c.api.ContainerCreate(ctx, createOpts)
	if err != nil {
		return "", fmt.Errorf("dockerclient: container create: %w", err)
	}

	if _, err := c.api.ContainerStart(ctx, created.ID, mobyclient.ContainerStartOptions{}); err != nil {
		return created.ID, fmt.Errorf("dockerclient: container start %s: %w", created.ID, err)
	}

	return created.ID, nil
}

// Stop sends SIGTERM with a 2-second grace period, then removes the container.
// Missing-container errors are swallowed so Close is idempotent.
func (c *Client) Stop(ctx context.Context, containerID string) error {
	timeout := 2
	_, err := c.api.ContainerStop(ctx, containerID, mobyclient.ContainerStopOptions{Timeout: &timeout})
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("dockerclient: container stop %s: %w", containerID, err)
	}

	if _, err := c.api.ContainerRemove(ctx, containerID, mobyclient.ContainerRemoveOptions{}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("dockerclient: container remove %s: %w", containerID, err)
	}
	return nil
}

// ContainerAlive reports whether the container is running. Returns (false, nil)
// when the container no longer exists.
func (c *Client) ContainerAlive(ctx context.Context, containerID string) (bool, error) {
	res, err := c.api.ContainerInspect(ctx, containerID, mobyclient.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("dockerclient: container inspect %s: %w", containerID, err)
	}
	if res.Container.State == nil {
		return false, nil
	}
	return res.Container.State.Running, nil
}

// buildContainerCreateOptions translates CreateOptions into the SDK request.
// Pure function so tests can assert the wiring without a daemon.
func buildContainerCreateOptions(opts CreateOptions) mobyclient.ContainerCreateOptions {
	return mobyclient.ContainerCreateOptions{
		Name:       opts.Name,
		Config:     buildContainerConfig(opts),
		HostConfig: buildHostConfig(opts),
	}
}

func buildContainerConfig(opts CreateOptions) *container.Config {
	cfg := &container.Config{
		Image:      opts.Image,
		Labels:     opts.Labels,
		Entrypoint: []string{"/bin/sh"},
		Cmd:        []string{"-c", "tail -f /dev/null"},
	}
	if opts.WorkspaceMount != "" {
		cfg.WorkingDir = opts.WorkspaceMount
	}
	cfg.Env = envSlice(opts.Env)
	return cfg
}

func buildHostConfig(opts CreateOptions) *container.HostConfig {
	hc := &container.HostConfig{
		NetworkMode: mapNetworkMode(opts.NetworkMode),
		Mounts:      buildMounts(opts),
	}
	return hc
}

func mapNetworkMode(m NetworkMode) container.NetworkMode {
	switch m {
	case NetworkDisabled:
		return container.NetworkMode("none")
	default:
		return container.NetworkMode("")
	}
}

func buildMounts(opts CreateOptions) []mount.Mount {
	n := len(opts.ReadOnlyMounts)
	if opts.WorkspaceHost != "" && opts.WorkspaceMount != "" {
		n++
	}
	mounts := make([]mount.Mount, 0, n)

	if opts.WorkspaceHost != "" && opts.WorkspaceMount != "" {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: opts.WorkspaceHost,
			Target: opts.WorkspaceMount,
		})
	}
	for _, m := range opts.ReadOnlyMounts {
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.HostPath,
			Target:   m.ContainerPath,
			ReadOnly: true,
		})
	}
	return mounts
}

// envSlice returns env in deterministic KEY=VALUE form sorted by key.
// The daemon accepts any order, but deterministic output simplifies testing
// and telemetry.
func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s=%s", k, env[k]))
	}
	return out
}
