package dockerclient

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
)

// stubAPI implements the whole API surface with zero-value no-ops so a test can
// override only the exec methods it exercises.
type stubAPI struct{}

func (stubAPI) ServerVersion(context.Context, mobyclient.ServerVersionOptions) (mobyclient.ServerVersionResult, error) {
	return mobyclient.ServerVersionResult{}, nil
}

func (stubAPI) ImageInspect(context.Context, string, ...mobyclient.ImageInspectOption) (mobyclient.ImageInspectResult, error) {
	return mobyclient.ImageInspectResult{}, nil
}

func (stubAPI) ImagePull(context.Context, string, mobyclient.ImagePullOptions) (mobyclient.ImagePullResponse, error) {
	return nil, nil
}

func (stubAPI) ContainerCreate(context.Context, mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error) {
	return mobyclient.ContainerCreateResult{}, nil
}

func (stubAPI) ContainerStart(context.Context, string, mobyclient.ContainerStartOptions) (mobyclient.ContainerStartResult, error) {
	return mobyclient.ContainerStartResult{}, nil
}

func (stubAPI) ContainerStop(context.Context, string, mobyclient.ContainerStopOptions) (mobyclient.ContainerStopResult, error) {
	return mobyclient.ContainerStopResult{}, nil
}

func (stubAPI) ContainerRemove(context.Context, string, mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
	return mobyclient.ContainerRemoveResult{}, nil
}

func (stubAPI) ContainerInspect(context.Context, string, mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
	return mobyclient.ContainerInspectResult{}, nil
}

func (stubAPI) ContainerList(context.Context, mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error) {
	return mobyclient.ContainerListResult{}, nil
}

func (stubAPI) VolumeCreate(context.Context, mobyclient.VolumeCreateOptions) (mobyclient.VolumeCreateResult, error) {
	return mobyclient.VolumeCreateResult{}, nil
}

func (stubAPI) VolumeList(context.Context, mobyclient.VolumeListOptions) (mobyclient.VolumeListResult, error) {
	return mobyclient.VolumeListResult{}, nil
}

func (stubAPI) VolumeRemove(context.Context, string, mobyclient.VolumeRemoveOptions) (mobyclient.VolumeRemoveResult, error) {
	return mobyclient.VolumeRemoveResult{}, nil
}

func (stubAPI) ExecCreate(context.Context, string, mobyclient.ExecCreateOptions) (mobyclient.ExecCreateResult, error) {
	return mobyclient.ExecCreateResult{}, nil
}

func (stubAPI) ExecAttach(context.Context, string, mobyclient.ExecAttachOptions) (mobyclient.ExecAttachResult, error) {
	return mobyclient.ExecAttachResult{}, nil
}

func (stubAPI) ExecInspect(context.Context, string, mobyclient.ExecInspectOptions) (mobyclient.ExecInspectResult, error) {
	return mobyclient.ExecInspectResult{Running: true}, nil
}

func (stubAPI) Close() error { return nil }

// silentPeerAPI hands out a hijacked connection whose peer never writes a frame,
// modelling a helper that hangs before responding. ExecInspect always reports
// the exec as still running so only cancellation can end the wait.
type silentPeerAPI struct {
	stubAPI
	creates int32
	peer    net.Conn // retained so the pipe stays open until the test ends
}

func (a *silentPeerAPI) ExecCreate(context.Context, string, mobyclient.ExecCreateOptions) (mobyclient.ExecCreateResult, error) {
	atomic.AddInt32(&a.creates, 1)
	return mobyclient.ExecCreateResult{ID: "exec"}, nil
}

func (a *silentPeerAPI) ExecAttach(context.Context, string, mobyclient.ExecAttachOptions) (mobyclient.ExecAttachResult, error) {
	client, peer := net.Pipe()
	a.peer = peer // never written to: the peer emits no frame
	return mobyclient.ExecAttachResult{HijackedResponse: mobyclient.NewHijackedResponse(client, "application/vnd.docker.raw-stream")}, nil
}

func TestExecCancellationUnblocksPromptly(t *testing.T) {
	api := &silentPeerAPI{}
	client := NewWithAPI(api)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := client.Exec(ctx, ExecOptions{ContainerID: "c", Command: []string{"stella-fs"}})
		done <- err
	}()

	// Give the blocking demux a moment to park, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Exec returned nil error on cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Exec did not unblock within 2s of cancellation")
	}
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("ExecCreate called %d times, want exactly one", got)
	}
}

func TestStartExecCancellationUnblocksPromptly(t *testing.T) {
	api := &silentPeerAPI{}
	client := NewWithAPI(api)
	ctx, cancel := context.WithCancel(context.Background())

	handle, err := client.StartExec(ctx, ExecOptions{ContainerID: "c", Command: []string{"stella-fs"}})
	if err != nil {
		t.Fatalf("StartExec: %v", err)
	}

	// A read that blocks until either a frame arrives or the transport is torn
	// down. Cancellation must unblock it promptly.
	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 4)
		_, err := handle.Stdout.Read(buf)
		readDone <- err
	}()
	waitDone := make(chan error, 1)
	go func() {
		_, err := handle.Wait()
		waitDone <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	for _, ch := range []struct {
		name string
		c    chan error
	}{{"stdout read", readDone}, {"Wait", waitDone}} {
		select {
		case err := <-ch.c:
			if err == nil {
				t.Fatalf("%s returned nil error on cancellation", ch.name)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not unblock within 2s of cancellation", ch.name)
		}
	}
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("ExecCreate called %d times, want exactly one", got)
	}
	// Teardown after cancellation completes without deadlock or double-close.
	if err := handle.Kill(); err != nil {
		t.Fatalf("Kill after cancel: %v", err)
	}
	if err := handle.Release(); err != nil {
		t.Fatalf("Release after cancel: %v", err)
	}
}
