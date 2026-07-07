package docker

import (
	"context"
	"sync/atomic"
	"testing"

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
