package dockerclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
)

func TestIsContainerStale(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	cases := []struct {
		name      string
		status    string
		labels    map[string]string
		ownerGone bool
		want      bool
	}{
		{"exited is stale", "exited", nil, false, true},
		{"dead is stale", "dead", nil, false, true},
		{"created with live owner is preserved", "created", nil, false, false},
		{"created with dead owner is stale", "created", nil, true, true},
		{"running live owner is preserved", "running", nil, false, false},
		{"running dead owner is stale", "running", nil, true, true},
		{"paused dead owner is stale", "paused", nil, true, true},
		{"unknown no createdAt", "unknown", nil, true, false},
		{"unknown old createdAt stale", "unknown", map[string]string{LabelCreatedAt: old}, false, true},
		{"unknown bad createdAt not stale", "unknown", map[string]string{LabelCreatedAt: "bad-date"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			got := isContainerStale(tc.status, tc.labels, Owner{}, func() bool {
				called = true
				return tc.ownerGone
			})
			if got != tc.want {
				t.Fatalf("isContainerStale() = %v, want %v", got, tc.want)
			}
			if called != (tc.status == "created" || tc.status == "running" || tc.status == "paused") {
				t.Fatalf("owner check called = %v for %q", called, tc.status)
			}
		})
	}
}

type orphanAPI struct {
	API
	inspect map[string]inspectResponse
	removed []string
}

type inspectResponse struct {
	result mobyclient.ContainerInspectResult
	err    error
}

func (f *orphanAPI) ContainerInspect(_ context.Context, id string, _ mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
	res, ok := f.inspect[id]
	if !ok {
		return mobyclient.ContainerInspectResult{}, errdefs.ErrNotFound
	}
	return res.result, res.err
}

func (f *orphanAPI) ContainerRemove(_ context.Context, id string, _ mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
	f.removed = append(f.removed, id)
	return mobyclient.ContainerRemoveResult{}, nil
}

func sandboxInspect(status string, labels map[string]string) mobyclient.ContainerInspectResult {
	return mobyclient.ContainerInspectResult{Container: container.InspectResponse{
		Config: &container.Config{Labels: labels},
		State:  &container.State{Status: container.ContainerState(status)},
	}}
}

func ownerInspect(running bool) mobyclient.ContainerInspectResult {
	return mobyclient.ContainerInspectResult{Container: container.InspectResponse{
		State: &container.State{Running: running},
	}}
}

func TestCleanupContainerContainerOwnerLiveness(t *testing.T) {
	const sandboxID = "sandbox"
	cases := []struct {
		name    string
		current Owner
		ownerID string
		owner   inspectResponse
		want    bool
	}{
		{
			name:    "same container ID is a prior process leftover",
			current: Owner{Kind: OwnerKindContainer, ID: "self"},
			ownerID: "self",
			want:    true,
		},
		{
			name:    "live foreign owner is preserved",
			current: Owner{Kind: OwnerKindContainer, ID: "self"},
			ownerID: "peer",
			owner:   inspectResponse{result: ownerInspect(true)},
			want:    false,
		},
		{
			name:    "missing foreign owner is removed",
			current: Owner{Kind: OwnerKindContainer, ID: "self"},
			ownerID: "gone",
			owner:   inspectResponse{err: errdefs.ErrNotFound},
			want:    true,
		},
		{
			name:    "stopped foreign owner is removed",
			current: Owner{Kind: OwnerKindContainer, ID: "self"},
			ownerID: "stopped",
			owner:   inspectResponse{result: ownerInspect(false)},
			want:    true,
		},
		{
			name:    "owner inspect error fails closed",
			current: Owner{Kind: OwnerKindContainer, ID: "self"},
			ownerID: "unavailable",
			owner:   inspectResponse{err: errors.New("daemon unavailable")},
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &orphanAPI{inspect: map[string]inspectResponse{
				sandboxID:  {result: sandboxInspect("running", map[string]string{LabelOwnerKind: OwnerKindContainer, LabelOwnerID: tc.ownerID})},
				tc.ownerID: tc.owner,
			}}
			cleanupContainer(context.Background(), NewWithAPI(api), sandboxID, tc.current)
			if got := len(api.removed) == 1; got != tc.want {
				t.Fatalf("removed = %v, want %v", api.removed, tc.want)
			}
		})
	}
}

func TestCleanupContainerCreatedForeignOwnerIsPreserved(t *testing.T) {
	api := &orphanAPI{inspect: map[string]inspectResponse{
		"sandbox":   {result: sandboxInspect("created", map[string]string{LabelOwnerKind: OwnerKindContainer, LabelOwnerID: "live-peer"})},
		"live-peer": {result: ownerInspect(true)},
	}}
	cleanupContainer(context.Background(), NewWithAPI(api), "sandbox", Owner{Kind: OwnerKindContainer, ID: "self"})
	if len(api.removed) != 0 {
		t.Fatalf("created sandbox with a live foreign owner removed: %v", api.removed)
	}
}

func TestCleanupContainerPIDOwnershipIsHostOnly(t *testing.T) {
	// Use a real, reaped child PID: unlike a guessed out-of-range value this has
	// consistent Signal(0) semantics across Unix kernels.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run short-lived process: %v", err)
	}
	deadPID := strconv.Itoa(cmd.ProcessState.Pid())
	cases := []struct {
		name    string
		labels  map[string]string
		current Owner
		want    bool
	}{
		{
			name:    "DooD preserves process owner",
			labels:  map[string]string{LabelOwnerKind: OwnerKindProcess, LabelOwnerID: deadPID},
			current: Owner{Kind: OwnerKindContainer, ID: "self"},
			want:    false,
		},
		{
			name:    "host removes same PID left by prior process",
			labels:  map[string]string{LabelOwnerKind: OwnerKindProcess, LabelOwnerID: strconv.Itoa(os.Getpid())},
			current: Owner{Kind: OwnerKindProcess, ID: strconv.Itoa(os.Getpid())},
			want:    true,
		},
		{
			name:    "host removes dead process owner",
			labels:  map[string]string{LabelOwnerKind: OwnerKindProcess, LabelOwnerID: deadPID},
			current: Owner{Kind: OwnerKindProcess, ID: "self"},
			want:    true,
		},
		{
			name:    "DooD preserves legacy PID owner",
			labels:  map[string]string{LabelOwnerPID: deadPID},
			current: Owner{Kind: OwnerKindContainer, ID: "self"},
			want:    false,
		},
		{
			name:    "host removes legacy same PID left by prior process",
			labels:  map[string]string{LabelOwnerPID: strconv.Itoa(os.Getpid())},
			current: Owner{Kind: OwnerKindProcess, ID: strconv.Itoa(os.Getpid())},
			want:    true,
		},
		{
			name:    "host removes legacy dead PID owner",
			labels:  map[string]string{LabelOwnerPID: deadPID},
			current: Owner{Kind: OwnerKindProcess, ID: "self"},
			want:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &orphanAPI{inspect: map[string]inspectResponse{
				"sandbox": {result: sandboxInspect("running", tc.labels)},
			}}
			cleanupContainer(context.Background(), NewWithAPI(api), "sandbox", tc.current)
			if got := len(api.removed) == 1; got != tc.want {
				t.Fatalf("removed = %v, want removal %v", api.removed, tc.want)
			}
		})
	}
}

func TestCleanupContainerDeadStateAlwaysRemoves(t *testing.T) {
	api := &orphanAPI{inspect: map[string]inspectResponse{
		"sandbox":   {result: sandboxInspect("exited", map[string]string{LabelOwnerKind: OwnerKindContainer, LabelOwnerID: "live-peer"})},
		"live-peer": {result: ownerInspect(true)},
	}}
	cleanupContainer(context.Background(), NewWithAPI(api), "sandbox", Owner{Kind: OwnerKindContainer, ID: "self"})
	if fmt.Sprint(api.removed) != "[sandbox]" {
		t.Fatalf("removed = %v, want [sandbox]", api.removed)
	}
}

func TestOwnerProcessGone(t *testing.T) {
	for _, pid := range []string{"", "0", "-1", "abc"} {
		if ownerProcessGone(pid) {
			t.Fatalf("ownerProcessGone(%q) = true, want false", pid)
		}
	}
	if ownerProcessGone(fmt.Sprintf("%d", os.Getpid())) {
		t.Fatal("current process should not be gone")
	}
}
