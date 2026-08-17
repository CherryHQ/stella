package dockerclient

import (
	"context"
	"errors"
	"io"
	"iter"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/containerd/errdefs"
	jsonstream "github.com/moby/moby/api/types/jsonstream"
	mobyclient "github.com/moby/moby/client"
)

type lifecycleAPI struct {
	API
	inspectFn          func(context.Context, string) (mobyclient.ImageInspectResult, error)
	pullFn             func(context.Context, string) (mobyclient.ImagePullResponse, error)
	createFn           func(context.Context, mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error)
	startFn            func(context.Context, string) (mobyclient.ContainerStartResult, error)
	stopFn             func(context.Context, string) (mobyclient.ContainerStopResult, error)
	removeFn           func(context.Context, string, mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error)
	containerInspectFn func(context.Context, string) (mobyclient.ContainerInspectResult, error)
}

func (f *lifecycleAPI) ImageInspect(ctx context.Context, image string, _ ...mobyclient.ImageInspectOption) (mobyclient.ImageInspectResult, error) {
	if f.inspectFn != nil {
		return f.inspectFn(ctx, image)
	}
	return mobyclient.ImageInspectResult{}, nil
}

func (f *lifecycleAPI) ImagePull(ctx context.Context, image string, _ mobyclient.ImagePullOptions) (mobyclient.ImagePullResponse, error) {
	if f.pullFn != nil {
		return f.pullFn(ctx, image)
	}
	return lifecyclePullResponse{}, nil
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

func (f *lifecycleAPI) ContainerStop(ctx context.Context, id string, _ mobyclient.ContainerStopOptions) (mobyclient.ContainerStopResult, error) {
	if f.stopFn != nil {
		return f.stopFn(ctx, id)
	}
	return mobyclient.ContainerStopResult{}, nil
}

func (f *lifecycleAPI) ContainerInspect(ctx context.Context, id string, _ mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
	if f.containerInspectFn != nil {
		return f.containerInspectFn(ctx, id)
	}
	return mobyclient.ContainerInspectResult{}, errdefs.ErrNotFound
}

type lifecyclePullResponse struct{}

func (lifecyclePullResponse) Read([]byte) (int, error) { return 0, io.EOF }
func (lifecyclePullResponse) Close() error             { return nil }
func (lifecyclePullResponse) Wait(context.Context) error {
	return nil
}

func (lifecyclePullResponse) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(yield func(jsonstream.Message, error) bool) {}
}

func TestCreateAndStartRemovesContainerWhenStartFails(t *testing.T) {
	startErr := errors.New("boom")
	var removedID string
	var removedForce bool
	api := &lifecycleAPI{
		startFn: func(context.Context, string) (mobyclient.ContainerStartResult, error) {
			return mobyclient.ContainerStartResult{}, startErr
		},
		stopFn: func(context.Context, string) (mobyclient.ContainerStopResult, error) {
			return mobyclient.ContainerStopResult{}, errors.New("start outcome unknown")
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cleanupCalled bool
	var cleanupErr error
	api := &lifecycleAPI{
		startFn: func(context.Context, string) (mobyclient.ContainerStartResult, error) {
			cancel()
			return mobyclient.ContainerStartResult{}, context.Canceled
		},
		stopFn: func(context.Context, string) (mobyclient.ContainerStopResult, error) {
			return mobyclient.ContainerStopResult{}, context.Canceled
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

func TestCreateOutcomeUnknownCleansDeterministicName(t *testing.T) {
	var removed string
	api := &lifecycleAPI{
		createFn: func(context.Context, mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error) {
			return mobyclient.ContainerCreateResult{}, errors.New("create response lost")
		},
		stopFn: func(context.Context, string) (mobyclient.ContainerStopResult, error) {
			return mobyclient.ContainerStopResult{}, errors.New("container may exist")
		},
		removeFn: func(_ context.Context, id string, opts mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
			removed = id
			if !opts.Force {
				t.Fatal("ambiguous create cleanup was not forced")
			}
			return mobyclient.ContainerRemoveResult{}, nil
		},
	}
	if id, err := NewWithAPI(api).CreateAndStart(t.Context(), CreateOptions{Image: "ready", Name: "stella-sandbox-known"}); err == nil || id != "" {
		t.Fatalf("CreateAndStart = %q/%v, want original failure", id, err)
	}
	if removed != "stella-sandbox-known" {
		t.Fatalf("cleaned container = %q, want deterministic name", removed)
	}
}

func TestStopForceRemovesAndProvesAbsenceAfterStopFailure(t *testing.T) {
	stopErr := errors.New("stop outcome unknown")
	var forced bool
	api := &lifecycleAPI{
		stopFn: func(context.Context, string) (mobyclient.ContainerStopResult, error) {
			return mobyclient.ContainerStopResult{}, stopErr
		},
		removeFn: func(ctx context.Context, id string, opts mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
			forced = opts.Force
			if ctx.Err() != nil || id != "sandbox" {
				t.Fatalf("force removal context/id = %v/%q", ctx.Err(), id)
			}
			return mobyclient.ContainerRemoveResult{}, nil
		},
	}
	if err := NewWithAPI(api).Stop(t.Context(), "sandbox"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !forced {
		t.Fatal("Stop did not force removal after an ambiguous stop")
	}
}

func TestStopFailsClosedWhenContainerRemains(t *testing.T) {
	api := &lifecycleAPI{
		stopFn: func(context.Context, string) (mobyclient.ContainerStopResult, error) {
			return mobyclient.ContainerStopResult{}, errors.New("stop failed")
		},
		removeFn: func(context.Context, string, mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
			return mobyclient.ContainerRemoveResult{}, errors.New("remove failed")
		},
		containerInspectFn: func(context.Context, string) (mobyclient.ContainerInspectResult, error) {
			return mobyclient.ContainerInspectResult{}, nil
		},
	}
	if err := NewWithAPI(api).Stop(t.Context(), "sandbox"); err == nil {
		t.Fatal("Stop proved absence while the container still exists")
	}
}

func TestStopAcceptsInspectNotFoundAfterAmbiguousRemove(t *testing.T) {
	api := &lifecycleAPI{
		removeFn: func(context.Context, string, mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
			return mobyclient.ContainerRemoveResult{}, errors.New("remove response lost")
		},
	}
	if err := NewWithAPI(api).Stop(t.Context(), "sandbox"); err != nil {
		t.Fatalf("Stop after authoritative not-found: %v", err)
	}
}

func TestCreateAndStartInvalidatesImageMemoAndRetriesCreateOnNotFound(t *testing.T) {
	const image = "pruned:latest"
	var createCalls atomic.Int32
	var inspectCalls atomic.Int32
	var pullCalls atomic.Int32
	api := &lifecycleAPI{
		inspectFn: func(context.Context, string) (mobyclient.ImageInspectResult, error) {
			inspectCalls.Add(1)
			return mobyclient.ImageInspectResult{}, errdefs.ErrNotFound
		},
		pullFn: func(context.Context, string) (mobyclient.ImagePullResponse, error) {
			pullCalls.Add(1)
			return lifecyclePullResponse{}, nil
		},
		createFn: func(context.Context, mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error) {
			if createCalls.Add(1) == 1 {
				return mobyclient.ContainerCreateResult{}, errdefs.ErrNotFound
			}
			return mobyclient.ContainerCreateResult{ID: "created-after-repull"}, nil
		},
	}
	client := NewWithAPI(api)

	id, err := client.CreateAndStart(context.Background(), CreateOptions{Image: image, Name: "test"})
	if err != nil {
		t.Fatalf("CreateAndStart: %v", err)
	}
	if id != "created-after-repull" {
		t.Fatalf("id = %q, want created-after-repull", id)
	}
	if got := createCalls.Load(); got != 2 {
		t.Fatalf("create calls = %d, want 2", got)
	}
	if got := inspectCalls.Load(); got != 2 {
		t.Fatalf("inspect calls = %d, want 2", got)
	}
	if got := pullCalls.Load(); got != 2 {
		t.Fatalf("pull calls = %d, want 2", got)
	}
	if !client.isImageReady(image) {
		t.Fatal("image should be memoized ready after retry pull")
	}
}

func TestEnsureImageReadySingleflightsConcurrentInspect(t *testing.T) {
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
