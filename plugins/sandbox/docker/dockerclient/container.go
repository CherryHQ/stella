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

// MountType identifies how Docker should mount Source into the container.
type MountType string

const (
	MountTypeBind   MountType = "bind"
	MountTypeVolume MountType = "volume"
)

// Mount represents a mount from a daemon-visible source to a container path.
// Bind mount sources are interpreted by the daemon, so when anna runs inside a
// container they must already be translated to daemon-visible paths before
// reaching this struct. Volume mount sources are Docker volume names.
type Mount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
	Type          MountType
}

// CreateOptions configures a new sandbox container.
type CreateOptions struct {
	Image          string
	WorkspaceHost  string      // absolute host path (daemon-side)
	WorkspaceMount string      // absolute in-container path (e.g. "/home/anna/workspace")
	ReadOnlyMounts []Mount     // host -> container, read-only
	NetworkMode    NetworkMode // disabled | allow_all
	Env            map[string]string
	User           string            // optional container user override
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

// ContainerState holds the container running state and last exit code.
type ContainerState struct {
	Running  bool
	ExitCode int
}

// InspectContainerState returns the running state and exit code of a container
// referenced by ID or name. Returns (nil, nil) when the container does not exist.
func (c *Client) InspectContainerState(ctx context.Context, containerRef string) (*ContainerState, error) {
	res, err := c.api.ContainerInspect(ctx, containerRef, mobyclient.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("dockerclient: container inspect %s: %w", containerRef, err)
	}
	if res.Container.State == nil {
		return &ContainerState{}, nil
	}
	return &ContainerState{
		Running:  res.Container.State.Running,
		ExitCode: res.Container.State.ExitCode,
	}, nil
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
		User:       opts.User,
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
			Type:     dockerMountType(m.Type),
			Source:   m.HostPath,
			Target:   m.ContainerPath,
			ReadOnly: m.ReadOnly,
		})
	}
	return mounts
}

func dockerMountType(t MountType) mount.Type {
	switch t {
	case MountTypeVolume:
		return mount.TypeVolume
	default:
		return mount.TypeBind
	}
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
