package dockerclient

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
)

type recoveryAPI struct {
	API
	mu         sync.Mutex
	list       []string
	labels     map[string]map[string]string
	listErr    error
	status     map[string]string
	inspectErr map[string]error
	removed    map[string]int
	force      bool
}

func (a *recoveryAPI) ContainerList(context.Context, mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.listErr != nil {
		return mobyclient.ContainerListResult{}, a.listErr
	}
	items := make([]container.Summary, 0, len(a.list))
	for _, id := range a.list {
		items = append(items, container.Summary{ID: id, Labels: a.labels[id]})
	}
	return mobyclient.ContainerListResult{Items: items}, nil
}

func TestCaptureOrphanedContainersSkipsGenerationOwnedContainers(t *testing.T) {
	api := &recoveryAPI{
		list:    []string{"managed", "legacy"},
		labels:  map[string]map[string]string{"managed": {LabelGeneration: "7", LabelOwnerBootID: "boot-a"}},
		status:  map[string]string{"managed": "exited", "legacy": "exited"},
		removed: map[string]int{},
	}
	guard, err := CaptureOrphanedContainers(context.Background(), NewWithAPI(api), "scope")
	if err != nil {
		t.Fatal(err)
	}
	if clean, err := guard(context.Background()); err != nil || !clean {
		t.Fatalf("managed container snapshot: clean=%v err=%v, want clean", clean, err)
	}
	if api.removed["managed"] != 0 {
		t.Fatalf("managed container removed %d times, want 0", api.removed["managed"])
	}
	if api.removed["legacy"] != 1 {
		t.Fatalf("legacy container removed %d times, want once", api.removed["legacy"])
	}
}

func (a *recoveryAPI) ContainerInspect(_ context.Context, id string, _ mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.inspectErr[id]; err != nil {
		return mobyclient.ContainerInspectResult{}, err
	}
	status, ok := a.status[id]
	if !ok {
		return mobyclient.ContainerInspectResult{}, errors.New("container disappeared")
	}
	return mobyclient.ContainerInspectResult{Container: container.InspectResponse{State: &container.State{Status: container.ContainerState(status)}}}, nil
}

func (a *recoveryAPI) ContainerRemove(_ context.Context, id string, opts mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.force = opts.Force
	a.removed[id]++
	delete(a.status, id)
	return mobyclient.ContainerRemoveResult{}, nil
}

func TestCaptureOrphanedContainersKeepsInitialAlivePending(t *testing.T) {
	api := &recoveryAPI{list: []string{"initial"}, status: map[string]string{"initial": "running"}, removed: map[string]int{}}
	guard, err := CaptureOrphanedContainers(context.Background(), NewWithAPI(api), "scope")
	if err != nil {
		t.Fatal(err)
	}
	if clean, err := guard(context.Background()); err != nil || clean {
		t.Fatalf("alive initial container: clean=%v err=%v, want pending", clean, err)
	}
	api.mu.Lock()
	api.status["initial"] = "exited"
	api.mu.Unlock()
	if clean, err := guard(context.Background()); err != nil || !clean {
		t.Fatalf("terminal initial container: clean=%v err=%v, want clean", clean, err)
	}
	if api.removed["initial"] != 1 {
		t.Fatalf("removed initial container %d times, want once", api.removed["initial"])
	}
	if api.force {
		t.Fatal("orphan cleanup used force remove against a concurrently starting container")
	}
}

func TestCaptureOrphanedContainersIgnoresLaterHealthyContainers(t *testing.T) {
	api := &recoveryAPI{list: []string{"initial"}, status: map[string]string{"initial": "exited"}, removed: map[string]int{}}
	guard, err := CaptureOrphanedContainers(context.Background(), NewWithAPI(api), "scope")
	if err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	api.list = []string{"later-healthy"}
	api.status["later-healthy"] = "running"
	api.mu.Unlock()
	if clean, err := guard(context.Background()); err != nil || !clean {
		t.Fatalf("later healthy container affected startup snapshot: clean=%v err=%v", clean, err)
	}
	if api.removed["initial"] != 1 {
		t.Fatalf("removed initial container %d times, want once", api.removed["initial"])
	}
}

func TestCaptureOrphanedContainersFailsClosedOnInspectError(t *testing.T) {
	api := &recoveryAPI{
		list:       []string{"initial"},
		status:     map[string]string{"initial": "running"},
		inspectErr: map[string]error{"initial": errors.New("inspect failed")},
		removed:    map[string]int{},
	}
	guard, err := CaptureOrphanedContainers(context.Background(), NewWithAPI(api), "scope")
	if err != nil {
		t.Fatal(err)
	}
	if clean, err := guard(context.Background()); err == nil || clean {
		t.Fatalf("inspect failure: clean=%v err=%v, want fail-closed error", clean, err)
	}
}

func TestCaptureOrphanedContainersFailsClosedOnListError(t *testing.T) {
	listErr := errors.New("list failed")
	if _, err := CaptureOrphanedContainers(context.Background(), NewWithAPI(&recoveryAPI{listErr: listErr}), "scope"); !errors.Is(err, listErr) {
		t.Fatalf("capture list error = %v, want %v", err, listErr)
	}
}
