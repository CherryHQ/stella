package dockerclient

import (
	"context"
	"errors"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/system"
	mobyclient "github.com/moby/moby/client"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

type resourceAPI struct {
	API
	daemonID     string
	inspect      []resourceInspect
	inspectCalls int
	stopErr      error
	removeErr    error
	stopCalls    int
	removeCalls  int
}

type resourceInspect struct {
	result mobyclient.ContainerInspectResult
	err    error
}

func (f *resourceAPI) Info(context.Context, mobyclient.InfoOptions) (mobyclient.SystemInfoResult, error) {
	return mobyclient.SystemInfoResult{Info: system.Info{ID: f.daemonID}}, nil
}

func (f *resourceAPI) ContainerInspect(context.Context, string, mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
	if f.inspectCalls >= len(f.inspect) {
		return mobyclient.ContainerInspectResult{}, errdefs.ErrNotFound
	}
	item := f.inspect[f.inspectCalls]
	f.inspectCalls++
	return item.result, item.err
}

func (f *resourceAPI) ContainerStop(context.Context, string, mobyclient.ContainerStopOptions) (mobyclient.ContainerStopResult, error) {
	f.stopCalls++
	return mobyclient.ContainerStopResult{}, f.stopErr
}

func (f *resourceAPI) ContainerRemove(context.Context, string, mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
	f.removeCalls++
	return mobyclient.ContainerRemoveResult{}, f.removeErr
}

func resourceContainer(id string) mobyclient.ContainerInspectResult {
	return mobyclient.ContainerInspectResult{Container: container.InspectResponse{ID: id}}
}

func resourceIdentity() sandboxpkg.ResourceIdentity {
	return sandboxpkg.ResourceIdentity{Backend: resourceBackend, Authority: "daemon-a", Ref: "container-a"}
}

func TestResourceControllerProbeRequiresSameDaemon(t *testing.T) {
	api := &resourceAPI{daemonID: "daemon-b", inspect: []resourceInspect{{result: resourceContainer("container-a")}}}
	got, err := NewWithAPI(api).Controller().Probe(context.Background(), resourceIdentity())
	if !errors.Is(err, ErrResourceAuthorityMismatch) {
		t.Fatalf("Probe error = %v, want authority mismatch", err)
	}
	if got.State != sandboxpkg.ResourceStateUnknown {
		t.Fatalf("Probe state = %q, want unknown", got.State)
	}
	if api.inspectCalls != 0 {
		t.Fatalf("inspect calls = %d, want 0 on authority mismatch", api.inspectCalls)
	}
}

func TestResourceControllerProbeMissingIsAbsentOnlyOnSameDaemon(t *testing.T) {
	api := &resourceAPI{daemonID: "daemon-a", inspect: []resourceInspect{{err: errdefs.ErrNotFound}}}
	got, err := NewWithAPI(api).Controller().Probe(context.Background(), resourceIdentity())
	if err != nil {
		t.Fatalf("Probe error = %v", err)
	}
	if got.State != sandboxpkg.ResourceStateAbsent {
		t.Fatalf("Probe state = %q, want absent", got.State)
	}
}

func TestResourceControllerProbeRejectsAmbiguousInspect(t *testing.T) {
	api := &resourceAPI{daemonID: "daemon-a", inspect: []resourceInspect{{result: resourceContainer("other-container")}}}
	got, err := NewWithAPI(api).Controller().Probe(context.Background(), resourceIdentity())
	if !errors.Is(err, ErrResourceIdentityMismatch) {
		t.Fatalf("Probe error = %v, want identity mismatch", err)
	}
	if got.State != sandboxpkg.ResourceStateUnknown {
		t.Fatalf("Probe state = %q, want unknown", got.State)
	}
}

func TestResourceControllerTerminateProvesRemoval(t *testing.T) {
	api := &resourceAPI{
		daemonID: "daemon-a",
		inspect: []resourceInspect{
			{result: resourceContainer("container-a")},
			{err: errdefs.ErrNotFound},
		},
	}
	got, err := NewWithAPI(api).Controller().Terminate(context.Background(), resourceIdentity())
	if err != nil {
		t.Fatalf("Terminate error = %v", err)
	}
	if got.State != sandboxpkg.ResourceStateAbsent {
		t.Fatalf("Terminate state = %q, want absent", got.State)
	}
	if api.stopCalls != 1 || api.removeCalls != 1 {
		t.Fatalf("stop/remove calls = %d/%d, want 1/1", api.stopCalls, api.removeCalls)
	}
}

func TestResourceControllerProbeErrorsAreUnknown(t *testing.T) {
	wantErr := errors.New("daemon unavailable")
	api := &resourceAPI{daemonID: "daemon-a", inspect: []resourceInspect{{err: wantErr}}}
	got, err := NewWithAPI(api).Controller().Probe(context.Background(), resourceIdentity())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Probe error = %v, want %v", err, wantErr)
	}
	if got.State != sandboxpkg.ResourceStateUnknown {
		t.Fatalf("Probe state = %q, want unknown", got.State)
	}
}
