package dockerclient

import (
	"context"
	"errors"
	"strings"
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
	daemonIDs    []string
	infoCalls    int
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
	if len(f.daemonIDs) > 0 {
		index := f.infoCalls
		if index >= len(f.daemonIDs) {
			index = len(f.daemonIDs) - 1
		}
		f.infoCalls++
		return mobyclient.SystemInfoResult{Info: system.Info{ID: f.daemonIDs[index]}}, nil
	}
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
	return sandboxpkg.ResourceIdentity{Backend: resourceBackend, Authority: "daemon-a", Ref: strings.Repeat("a", 64)}
}

func TestResourceControllerProbeRequiresSameDaemon(t *testing.T) {
	api := &resourceAPI{daemonID: "daemon-b", inspect: []resourceInspect{{result: resourceContainer(strings.Repeat("a", 64))}}}
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

func TestResourceControllerProbeMissingRechecksAuthority(t *testing.T) {
	api := &resourceAPI{
		daemonIDs: []string{"daemon-a", "daemon-b"},
		inspect:   []resourceInspect{{err: errdefs.ErrNotFound}},
	}
	got, err := NewWithAPI(api).Controller().Probe(context.Background(), resourceIdentity())
	if !errors.Is(err, ErrResourceAuthorityMismatch) {
		t.Fatalf("Probe error = %v, want authority mismatch after inspect", err)
	}
	if got.State != sandboxpkg.ResourceStateUnknown {
		t.Fatalf("Probe state = %q, want unknown", got.State)
	}
}

func TestResourceControllerProbeRejectsAmbiguousInspect(t *testing.T) {
	api := &resourceAPI{daemonID: "daemon-a", inspect: []resourceInspect{{result: resourceContainer(strings.Repeat("b", 64))}}}
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
			{result: resourceContainer(strings.Repeat("a", 64))},
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

func TestResourceControllerRejectsShortContainerReference(t *testing.T) {
	identity := resourceIdentity()
	identity.Ref = "container-a"
	api := &resourceAPI{daemonID: "daemon-a"}
	got, err := NewWithAPI(api).Controller().Probe(context.Background(), identity)
	if err == nil {
		t.Fatal("Probe succeeded with a non-durable container reference")
	}
	if got.State != sandboxpkg.ResourceStateUnknown {
		t.Fatalf("Probe state = %q, want unknown", got.State)
	}
	if api.infoCalls != 0 {
		t.Fatalf("Info calls = %d, want 0 for invalid identity", api.infoCalls)
	}
}
