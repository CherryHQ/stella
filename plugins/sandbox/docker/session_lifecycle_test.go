package docker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"

	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

type stopCountingAPI struct {
	noopAPI
	stops   atomic.Int32
	removes atomic.Int32
}

func (f *stopCountingAPI) ContainerStop(context.Context, string, mobyclient.ContainerStopOptions) (mobyclient.ContainerStopResult, error) {
	f.stops.Add(1)
	return mobyclient.ContainerStopResult{}, nil
}

func (f *stopCountingAPI) ContainerRemove(context.Context, string, mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
	f.removes.Add(1)
	return mobyclient.ContainerRemoveResult{}, nil
}

type blockingStopAPI struct {
	noopAPI
	stopStarted chan struct{}
	releaseStop chan struct{}
	stops       atomic.Int32
	removes     atomic.Int32
}

func (f *blockingStopAPI) ContainerStop(ctx context.Context, _ string, _ mobyclient.ContainerStopOptions) (mobyclient.ContainerStopResult, error) {
	f.stops.Add(1)
	close(f.stopStarted)
	select {
	case <-f.releaseStop:
		return mobyclient.ContainerStopResult{}, nil
	case <-ctx.Done():
		return mobyclient.ContainerStopResult{}, ctx.Err()
	}
}

func (f *blockingStopAPI) ContainerRemove(context.Context, string, mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
	f.removes.Add(1)
	return mobyclient.ContainerRemoveResult{}, nil
}

func TestCloseDoesNotHoldSessionLockDuringStop(t *testing.T) {
	api := &blockingStopAPI{stopStarted: make(chan struct{}), releaseStop: make(chan struct{})}
	s := &dockerSession{
		id:          "session-1",
		client:      dockerclient.NewWithAPI(api),
		containerID: "container-1",
		done:        make(chan struct{}),
	}

	closeErr := make(chan error, 1)
	go func() { closeErr <- s.Close() }()

	select {
	case <-api.stopStarted:
	case <-time.After(time.Second):
		t.Fatal("Close did not start container stop")
	}

	aliveResult := make(chan bool, 1)
	go func() { aliveResult <- s.Alive() }()
	select {
	case alive := <-aliveResult:
		if alive {
			t.Fatal("session should be marked closed before Stop returns")
		}
	case <-time.After(200 * time.Millisecond):
		close(api.releaseStop)
		if err := <-closeErr; err != nil {
			t.Fatalf("Close cleanup: %v", err)
		}
		t.Fatal("Alive blocked while Close was waiting for Stop")
	}

	close(api.releaseStop)
	if err := <-closeErr; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if api.stops.Load() != 1 || api.removes.Load() != 1 {
		t.Fatalf("cleanup calls = stop %d remove %d, want 1/1", api.stops.Load(), api.removes.Load())
	}
}

func TestCloseFromWatcherReapsContainerAndExplicitCloseDoesNotDoubleRemove(t *testing.T) {
	api := &stopCountingAPI{}
	s := &dockerSession{
		id:          "session-1",
		client:      dockerclient.NewWithAPI(api),
		containerID: "container-1",
		done:        make(chan struct{}),
	}

	s.closeFromWatcher("container_exited", nil)
	if s.Alive() {
		t.Fatal("session should be closed after watcher close")
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("done channel should be closed")
	}
	if api.stops.Load() != 1 || api.removes.Load() != 1 {
		t.Fatalf("cleanup calls = stop %d remove %d, want 1/1", api.stops.Load(), api.removes.Load())
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close after watcher close: %v", err)
	}
	if api.stops.Load() != 1 || api.removes.Load() != 1 {
		t.Fatalf("explicit Close double-cleaned: stop %d remove %d", api.stops.Load(), api.removes.Load())
	}
}
