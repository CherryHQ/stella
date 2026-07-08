package docker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"

	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

type vanishedInstallerAPI struct{ noopAPI }

func (vanishedInstallerAPI) ContainerInspect(context.Context, string, mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
	return mobyclient.ContainerInspectResult{}, errdefs.ErrNotFound
}

func TestWaitForToolCacheFailsClosedWhenInstallerVanishedButVolumeNotReady(t *testing.T) {
	client := dockerclient.NewWithAPI(vanishedInstallerAPI{})
	verifyErr := errors.New("ready marker missing")
	_, err := waitForToolCache(context.Background(), client, "installer", &userToolCache{VolumeName: "vol", BinPath: containerUserToolsBin}, func(context.Context) error {
		return verifyErr
	})
	if err == nil {
		t.Fatal("expected wait to fail when verifier rejects the cache")
	}
	if !strings.Contains(err.Error(), "cache is not ready") {
		t.Fatalf("error = %v, want cache-not-ready context", err)
	}
}

func TestEnsureUserToolCacheSingleflightsConcurrentInstall(t *testing.T) {
	resetToolCacheMemoForTest()
	defer resetToolCacheMemoForTest()

	var installCalls atomic.Int32
	unblock := make(chan struct{})
	installToolCacheFn = func(ctx context.Context, _ *dockerclient.Client, _ Config, _ string, _ string, cache *userToolCache) (*userToolCache, error) {
		installCalls.Add(1)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-unblock:
			return cache, nil
		}
	}

	client := dockerclient.NewWithAPI(noopAPI{})
	cfg := Config{Image: "tool-cache-singleflight:latest", UserToolBinaries: []ToolBinary{{Name: "uv", Tool: "uv"}}}

	const callers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			<-start
			_, err := ensureUserToolCache(context.Background(), client, cfg)
			errs <- err
		})
	}
	close(start)
	for installCalls.Load() == 0 {
	}
	close(unblock)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("ensureUserToolCache: %v", err)
		}
	}
	if got := installCalls.Load(); got != 1 {
		t.Fatalf("install calls = %d, want 1", got)
	}
}

func TestSelectStaleToolCacheVolumes(t *testing.T) {
	now := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	old := now.Add(-toolCacheGCAgeThreshold - time.Hour).Format(time.RFC3339)
	recent := now.Add(-time.Hour).Format(time.RFC3339)
	volumes := []volume.Volume{
		{Name: "remove-old-unused", Labels: map[string]string{toolCacheLabel: "true", toolCacheCreatedAtLabel: old}},
		{Name: "keep-recent", Labels: map[string]string{toolCacheLabel: "true", toolCacheCreatedAtLabel: recent}},
		{Name: "keep-in-use", Labels: map[string]string{toolCacheLabel: "true", toolCacheCreatedAtLabel: old}},
		{Name: "keep-refcount", Labels: map[string]string{toolCacheLabel: "true", toolCacheCreatedAtLabel: old}, UsageData: &volume.UsageData{RefCount: 1}},
		{Name: "keep-unlabelled", Labels: map[string]string{toolCacheCreatedAtLabel: old}},
		{Name: "keep-missing-created", Labels: map[string]string{toolCacheLabel: "true"}},
	}
	containers := []container.Summary{
		{Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: "keep-in-use"}}},
	}

	got := selectStaleToolCacheVolumes(now, volumes, containers)
	if len(got) != 1 || got[0] != "remove-old-unused" {
		t.Fatalf("selected = %v, want [remove-old-unused]", got)
	}
}
