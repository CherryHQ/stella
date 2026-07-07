package dockerclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"
	"golang.org/x/sync/singleflight"
)

// API is the subset of moby/moby/client.APIClient this package uses.
// Kept narrow so tests can substitute a fake without pulling the full SDK
// surface.
type API interface {
	ServerVersion(ctx context.Context, opts mobyclient.ServerVersionOptions) (mobyclient.ServerVersionResult, error)
	ImageInspect(ctx context.Context, image string, opts ...mobyclient.ImageInspectOption) (mobyclient.ImageInspectResult, error)
	ImagePull(ctx context.Context, ref string, opts mobyclient.ImagePullOptions) (mobyclient.ImagePullResponse, error)

	ContainerCreate(ctx context.Context, opts mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, container string, opts mobyclient.ContainerStartOptions) (mobyclient.ContainerStartResult, error)
	ContainerStop(ctx context.Context, container string, opts mobyclient.ContainerStopOptions) (mobyclient.ContainerStopResult, error)
	ContainerRemove(ctx context.Context, container string, opts mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error)
	ContainerInspect(ctx context.Context, container string, opts mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error)
	ContainerList(ctx context.Context, opts mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error)

	VolumeCreate(ctx context.Context, opts mobyclient.VolumeCreateOptions) (mobyclient.VolumeCreateResult, error)
	VolumeList(ctx context.Context, opts mobyclient.VolumeListOptions) (mobyclient.VolumeListResult, error)
	VolumeRemove(ctx context.Context, volumeID string, opts mobyclient.VolumeRemoveOptions) (mobyclient.VolumeRemoveResult, error)

	ExecCreate(ctx context.Context, container string, opts mobyclient.ExecCreateOptions) (mobyclient.ExecCreateResult, error)
	ExecAttach(ctx context.Context, execID string, opts mobyclient.ExecAttachOptions) (mobyclient.ExecAttachResult, error)
	ExecInspect(ctx context.Context, execID string, opts mobyclient.ExecInspectOptions) (mobyclient.ExecInspectResult, error)

	Close() error
}

// Client wraps a moby SDK client. Constructed from environment (DOCKER_HOST,
// DOCKER_API_VERSION, DOCKER_CERT_PATH, DOCKER_TLS_VERIFY); see the moby client
// docs for the full list.
type Client struct {
	api API

	imageReadyMu    sync.Mutex
	imageReady      map[string]struct{}
	imageReadyGroup singleflight.Group
}

// VersionInfo holds the minimal version data we care about. The shape is kept
// stable across the CLI→SDK migration so callers don't churn.
type VersionInfo struct {
	Server struct {
		APIVersion string
	}
	Client struct {
		Version string
	}
}

// New returns a Client configured from the process environment. API-version
// negotiation is enabled by default in the moby SDK.
func New() (*Client, error) {
	api, err := mobyclient.New(mobyclient.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("dockerclient: new moby client: %w", err)
	}
	return newClient(api), nil
}

// NewWithAPI constructs a Client with an injected API implementation. The
// moby SDK's *client.Client already satisfies API; callers typically use New()
// instead. Exported for tests and advanced callers that want to override the
// SDK client (e.g. to inject a TLS-wrapped instance).
func NewWithAPI(api API) *Client {
	return newClient(api)
}

func newClient(api API) *Client {
	return &Client{
		api:        api,
		imageReady: map[string]struct{}{},
	}
}

// Close releases any resources held by the client (HTTP connections, etc.).
func (c *Client) Close() error {
	if c.api == nil {
		return nil
	}
	return c.api.Close()
}

// Version queries the daemon for server + client version info. Used by
// preflight to confirm daemon reachability.
func (c *Client) Version(ctx context.Context) (*VersionInfo, error) {
	res, err := c.api.ServerVersion(ctx, mobyclient.ServerVersionOptions{})
	if err != nil {
		return nil, fmt.Errorf("dockerclient: server version: %w", err)
	}
	info := &VersionInfo{}
	info.Server.APIVersion = res.APIVersion
	info.Client.Version = mobyclient.MaxAPIVersion
	return info, nil
}

// ImageExists reports whether the image exists locally.
// Returns false (no error) when the daemon reports not-found; any other error
// surfaces.
func (c *Client) ImageExists(ctx context.Context, image string) (bool, error) {
	_, err := c.api.ImageInspect(ctx, image)
	if err == nil {
		return true, nil
	}
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("dockerclient: image inspect %s: %w", image, err)
}

func (c *Client) VolumeCreate(ctx context.Context, opts mobyclient.VolumeCreateOptions) (mobyclient.VolumeCreateResult, error) {
	res, err := c.api.VolumeCreate(ctx, opts)
	if err != nil {
		return mobyclient.VolumeCreateResult{}, fmt.Errorf("dockerclient: volume create %s: %w", opts.Name, err)
	}
	return res, nil
}

func (c *Client) VolumeList(ctx context.Context, opts mobyclient.VolumeListOptions) (mobyclient.VolumeListResult, error) {
	res, err := c.api.VolumeList(ctx, opts)
	if err != nil {
		return mobyclient.VolumeListResult{}, fmt.Errorf("dockerclient: volume list: %w", err)
	}
	return res, nil
}

func (c *Client) VolumeRemove(ctx context.Context, name string, opts mobyclient.VolumeRemoveOptions) error {
	if _, err := c.api.VolumeRemove(ctx, name, opts); err != nil {
		return fmt.Errorf("dockerclient: volume remove %s: %w", name, err)
	}
	return nil
}

func (c *Client) ContainerList(ctx context.Context, opts mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error) {
	res, err := c.api.ContainerList(ctx, opts)
	if err != nil {
		return mobyclient.ContainerListResult{}, fmt.Errorf("dockerclient: container list: %w", err)
	}
	return res, nil
}

// PullImage pulls an image, draining the JSON progress stream into slog.Info.
func (c *Client) PullImage(ctx context.Context, image string) error {
	resp, err := c.api.ImagePull(ctx, image, mobyclient.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("dockerclient: image pull %s: %w", image, err)
	}
	defer func() {
		if cerr := resp.Close(); cerr != nil {
			slog.Warn("dockerclient: image pull response close", "image", image, "error", cerr)
		}
	}()

	scanner := bufio.NewScanner(resp)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		slog.Info("docker pull", "image", image, "output", scanner.Text())
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("dockerclient: image pull %s: drain: %w", image, err)
	}
	if err := resp.Wait(ctx); err != nil {
		return fmt.Errorf("dockerclient: image pull %s: wait: %w", image, err)
	}
	return nil
}
