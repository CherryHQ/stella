package docker

import (
	"context"
	"net"

	"github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"
)

// noopAPI is a dockerclient.API implementation whose methods all return zero
// values. Tests embed it in a fake struct and override only the calls they
// care about.
type noopAPI struct{}

func (noopAPI) ServerVersion(context.Context, mobyclient.ServerVersionOptions) (mobyclient.ServerVersionResult, error) {
	return mobyclient.ServerVersionResult{}, nil
}

func (noopAPI) Info(context.Context, mobyclient.InfoOptions) (mobyclient.SystemInfoResult, error) {
	return mobyclient.SystemInfoResult{}, nil
}

func (noopAPI) ImageInspect(context.Context, string, ...mobyclient.ImageInspectOption) (mobyclient.ImageInspectResult, error) {
	return mobyclient.ImageInspectResult{}, nil
}

func (noopAPI) ImagePull(context.Context, string, mobyclient.ImagePullOptions) (mobyclient.ImagePullResponse, error) {
	return nil, nil
}

func (noopAPI) ContainerCreate(context.Context, mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error) {
	return mobyclient.ContainerCreateResult{}, nil
}

func (noopAPI) ContainerStart(context.Context, string, mobyclient.ContainerStartOptions) (mobyclient.ContainerStartResult, error) {
	return mobyclient.ContainerStartResult{}, nil
}

func (noopAPI) ContainerStop(context.Context, string, mobyclient.ContainerStopOptions) (mobyclient.ContainerStopResult, error) {
	return mobyclient.ContainerStopResult{}, nil
}

func (noopAPI) ContainerRemove(context.Context, string, mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
	return mobyclient.ContainerRemoveResult{}, nil
}

func (noopAPI) ContainerInspect(context.Context, string, mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
	return mobyclient.ContainerInspectResult{}, errdefs.ErrNotFound
}

func (noopAPI) ContainerList(context.Context, mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error) {
	return mobyclient.ContainerListResult{}, nil
}

func (noopAPI) VolumeCreate(context.Context, mobyclient.VolumeCreateOptions) (mobyclient.VolumeCreateResult, error) {
	return mobyclient.VolumeCreateResult{}, nil
}

func (noopAPI) VolumeList(context.Context, mobyclient.VolumeListOptions) (mobyclient.VolumeListResult, error) {
	return mobyclient.VolumeListResult{}, nil
}

func (noopAPI) VolumeRemove(context.Context, string, mobyclient.VolumeRemoveOptions) (mobyclient.VolumeRemoveResult, error) {
	return mobyclient.VolumeRemoveResult{}, nil
}

func (noopAPI) ExecCreate(context.Context, string, mobyclient.ExecCreateOptions) (mobyclient.ExecCreateResult, error) {
	return mobyclient.ExecCreateResult{}, nil
}

func (noopAPI) ExecAttach(context.Context, string, mobyclient.ExecAttachOptions) (mobyclient.ExecAttachResult, error) {
	clientConn, serverConn := net.Pipe()
	_ = serverConn.Close()
	return mobyclient.ExecAttachResult{HijackedResponse: mobyclient.NewHijackedResponse(clientConn, "application/vnd.docker.raw-stream")}, nil
}

func (noopAPI) ExecInspect(context.Context, string, mobyclient.ExecInspectOptions) (mobyclient.ExecInspectResult, error) {
	return mobyclient.ExecInspectResult{}, nil
}

func (noopAPI) Close() error { return nil }
