package dockerclient

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	mobyclient "github.com/moby/moby/client"
)

type lifecycleAPI struct {
	API
	inspectFn func(context.Context, string) (mobyclient.ImageInspectResult, error)
	createFn  func(context.Context, mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error)
	startFn   func(context.Context, string) (mobyclient.ContainerStartResult, error)
	removeFn  func(context.Context, string, mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error)
}

func (f *lifecycleAPI) ImageInspect(ctx context.Context, image string, _ ...mobyclient.ImageInspectOption) (mobyclient.ImageInspectResult, error) {
	if f.inspectFn != nil {
		return f.inspectFn(ctx, image)
	}
	return mobyclient.ImageInspectResult{}, nil
}

func (f *lifecycleAPI) ContainerCreate(ctx context.Context, opts mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error) {
	if f.createFn != nil {
		return f.createFn(ctx, opts)
	}
	return mobyclient.ContainerCreateResult{ID: "created-id"}, nil
}

func (f *lifecycleAPI) ContainerStart(ctx context.Context, id string, opts mobyclient.ContainerStartOptions) (mobyclient.ContainerStartResult, error) {
	if f.startFn != nil {
		return f.startFn(ctx, id)
	}
	return mobyclient.ContainerStartResult{}, nil
}

func (f *lifecycleAPI) ContainerRemove(ctx context.Context, id string, opts mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
	if f.removeFn != nil {
		return f.removeFn(ctx, id, opts)
	}
	return mobyclient.ContainerRemoveResult{}, nil
}

func TestCreateAndStartRemovesContainerWhenStartFails(t *testing.T) {
	resetImageReadyForTest()
	startErr := errors.New("boom")
	var removedID string
	var removedForce bool
	api := &lifecycleAPI{
		startFn: func(context.Context, string) (mobyclient.ContainerStartResult, error) {
			return mobyclient.ContainerStartResult{}, startErr
		},
		removeFn: func(_ context.Context, id string, opts mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
			removedID = id
			removedForce = opts.Force
			return mobyclient.ContainerRemoveResult{}, nil
		},
	}
	client := NewWithAPI(api)

	id, err := client.CreateAndStart(context.Background(), CreateOptions{Image: "start-fails:latest", Name: "test"})
	if err == nil {
		t.Fatal("expected start error")
	}
	if id != "" {
		t.Fatalf("id = %q, want empty on start failure", id)
	}
	if removedID != "created-id" || !removedForce {
		t.Fatalf("remove = (%q, force=%v), want created-id force=true", removedID, removedForce)
	}
}

func TestCreateAndStartRemovesContainerWithFreshContextWhenStartCancelsCaller(t *testing.T) {
	resetImageReadyForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cleanupCalled bool
	var cleanupErr error
	api := &lifecycleAPI{
		startFn: func(context.Context, string) (mobyclient.ContainerStartResult, error) {
			cancel()
			return mobyclient.ContainerStartResult{}, context.Canceled
		},
		removeFn: func(ctx context.Context, id string, opts mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
			cleanupCalled = true
			cleanupErr = ctx.Err()
			if id != "created-id" || !opts.Force {
				t.Fatalf("remove = (%q, force=%v), want created-id force=true", id, opts.Force)
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("cleanup context should have a deadline")
			}
			return mobyclient.ContainerRemoveResult{}, nil
		},
	}
	client := NewWithAPI(api)

	id, err := client.CreateAndStart(ctx, CreateOptions{Image: "start-cancels:latest", Name: "test"})
	if err == nil {
		t.Fatal("expected start error")
	}
	if id != "" {
		t.Fatalf("id = %q, want empty on start failure", id)
	}
	if !cleanupCalled {
		t.Fatal("expected cleanup remove to run")
	}
	if cleanupErr != nil {
		t.Fatalf("cleanup context err = %v, want nil", cleanupErr)
	}
}

func TestEnsureImageReadySingleflightsConcurrentInspect(t *testing.T) {
	resetImageReadyForTest()
	var inspectCalls atomic.Int32
	unblock := make(chan struct{})
	api := &lifecycleAPI{
		inspectFn: func(ctx context.Context, image string) (mobyclient.ImageInspectResult, error) {
			inspectCalls.Add(1)
			select {
			case <-ctx.Done():
				return mobyclient.ImageInspectResult{}, ctx.Err()
			case <-unblock:
				return mobyclient.ImageInspectResult{}, nil
			}
		},
	}
	client := NewWithAPI(api)

	const callers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			<-start
			errs <- client.EnsureImageReady(context.Background(), "singleflight-image:latest", "test")
		})
	}
	close(start)
	for inspectCalls.Load() == 0 {
	}
	close(unblock)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("EnsureImageReady: %v", err)
		}
	}
	if got := inspectCalls.Load(); got != 1 {
		t.Fatalf("inspect calls = %d, want 1", got)
	}
}
