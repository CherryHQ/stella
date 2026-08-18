package docker

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/system"
	mobyclient "github.com/moby/moby/client"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

type stopCountingAPI struct {
	noopAPI
	stops    atomic.Int32
	removes  atomic.Int32
	execs    atomic.Int32
	execUser string
}

func (f *stopCountingAPI) ContainerStop(context.Context, string, mobyclient.ContainerStopOptions) (mobyclient.ContainerStopResult, error) {
	f.stops.Add(1)
	return mobyclient.ContainerStopResult{}, nil
}

func (f *stopCountingAPI) ExecCreate(_ context.Context, _ string, opts mobyclient.ExecCreateOptions) (mobyclient.ExecCreateResult, error) {
	f.execs.Add(1)
	f.execUser = opts.User
	return mobyclient.ExecCreateResult{ID: "cleanup-exec"}, nil
}

func (f *stopCountingAPI) ContainerRemove(context.Context, string, mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
	f.removes.Add(1)
	return mobyclient.ContainerRemoveResult{}, nil
}

type sessionListAPI struct {
	noopAPI
	list mobyclient.ContainerListResult
	err  error
}

func (f *sessionListAPI) ContainerList(context.Context, mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error) {
	return f.list, f.err
}

type startFailAPI struct {
	noopAPI
	createOpts mobyclient.ContainerCreateOptions
	rootless   bool
}

func (f *startFailAPI) Info(context.Context, mobyclient.InfoOptions) (mobyclient.SystemInfoResult, error) {
	info := system.Info{CgroupDriver: "systemd"}
	if f.rootless {
		info.SecurityOptions = []string{"name=rootless"}
	}
	return mobyclient.SystemInfoResult{Info: info}, nil
}

func (f *startFailAPI) ContainerCreate(_ context.Context, opts mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error) {
	f.createOpts = opts
	return mobyclient.ContainerCreateResult{ID: "container-1"}, nil
}

func (startFailAPI) ContainerStart(context.Context, string, mobyclient.ContainerStartOptions) (mobyclient.ContainerStartResult, error) {
	return mobyclient.ContainerStartResult{}, errors.New("start failed")
}

type createCountingAPI struct {
	noopAPI
	creates atomic.Int32
}

func (f *createCountingAPI) ContainerCreate(context.Context, mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error) {
	f.creates.Add(1)
	return mobyclient.ContainerCreateResult{}, nil
}

type blockingStopAPI struct {
	noopAPI
	stopStarted chan struct{}
	releaseStop chan struct{}
	stopErr     error
	stops       atomic.Int32
	removes     atomic.Int32
}

func (f *blockingStopAPI) ContainerStop(ctx context.Context, _ string, _ mobyclient.ContainerStopOptions) (mobyclient.ContainerStopResult, error) {
	f.stops.Add(1)
	close(f.stopStarted)
	select {
	case <-f.releaseStop:
		return mobyclient.ContainerStopResult{}, f.stopErr
	case <-ctx.Done():
		return mobyclient.ContainerStopResult{}, ctx.Err()
	}
}

func (f *blockingStopAPI) ContainerRemove(context.Context, string, mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
	f.removes.Add(1)
	return mobyclient.ContainerRemoveResult{}, nil
}

func TestCleanupStaleSessionTempDirsKeepsLiveSession(t *testing.T) {
	stellaHome := t.TempDir()
	root := filepath.Join(stellaHome, "cache", "sandbox-tmp")
	live := filepath.Join(root, "sandbox-live")
	stale := filepath.Join(root, "sandbox-stale")
	for _, dir := range []string{live, stale} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-2 * staleSessionTempMinimumAge)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	api := &sessionListAPI{list: mobyclient.ContainerListResult{Items: []container.Summary{{Labels: map[string]string{dockerclient.LabelSessionID: "sandbox-live"}}}}}
	cleanupStaleSessionTempDirs(context.Background(), dockerclient.NewWithAPI(api), "scope", stellaHome)
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live session temp was removed: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale session temp remains: %v", err)
	}
}

func TestCleanupStaleSessionTempDirsKeepsYoungDirectoryAndFailsClosed(t *testing.T) {
	stellaHome := t.TempDir()
	root := filepath.Join(stellaHome, "cache", "sandbox-tmp")
	young := filepath.Join(root, "sandbox-young")
	if err := os.MkdirAll(young, 0o700); err != nil {
		t.Fatal(err)
	}
	cleanupStaleSessionTempDirs(context.Background(), dockerclient.NewWithAPI(&sessionListAPI{}), "scope", stellaHome)
	if _, err := os.Stat(young); err != nil {
		t.Fatalf("young peer temp was removed: %v", err)
	}

	old := filepath.Join(root, "sandbox-list-error")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-2 * staleSessionTempMinimumAge)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	cleanupStaleSessionTempDirs(context.Background(), dockerclient.NewWithAPI(&sessionListAPI{err: errors.New("list failed")}), "scope", stellaHome)
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("temp was removed after container-list failure: %v", err)
	}
}

func TestCreateSessionStoresNormalizedPolicyAndPrivateMountedTemp(t *testing.T) {
	api := &stopCountingAPI{}
	workspace := t.TempDir()
	imageBinHost := t.TempDir()
	builtinHost := t.TempDir()
	if err := os.WriteFile(filepath.Join(imageBinHost, "host-only"), []byte("wrong process view"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(builtinHost, "SKILL.md"), []byte("verified bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	factory := &dockerFactory{
		cfg: Config{Image: "test:latest", RuntimeMode: DockerSandboxModeHost},
		mountSources: map[string]string{
			workspaceMount:                    workspace,
			path.Join(stellaHomeMount, "bin"): imageBinHost,
			sandboxpkg.MountBuiltinSkills:     builtinHost,
		},
		clientFn: func() (*dockerclient.Client, error) { return dockerclient.NewWithAPI(api), nil },
	}
	session, err := factory.CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: workspaceMount,
			Mounts: []sandboxpkg.Mount{
				{SandboxPath: `\workspace\`, Access: sandboxpkg.MountReadWrite},
				{SandboxPath: path.Join(stellaHomeMount, "bin"), Access: sandboxpkg.MountReadOnly},
				{SandboxPath: sandboxpkg.MountBuiltinSkills, Access: sandboxpkg.MountReadOnly},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	policy := session.Policy()
	if got := policy.Filesystem.Mounts[0].SandboxPath; got != workspaceMount {
		t.Errorf("normalized SandboxPath = %q, want %q", got, workspaceMount)
	}
	tempDir := policy.Env[sandboxpkg.EnvTempDir]
	if tempDir != "/tmp" {
		t.Fatalf("policy TMPDIR = %q, want canonical container path /tmp", tempDir)
	}
	if err := session.Files().WriteFile("/tmp/file", []byte("ok"), 0o600); err != nil {
		t.Fatalf("Files.WriteFile(/tmp/file): %v", err)
	}
	physicalTemp := session.(*dockerSession).ownedTempDir
	if got, err := os.ReadFile(filepath.Join(physicalTemp, "file")); err != nil || string(got) != "ok" {
		t.Errorf("physical temp file = %q, %v", got, err)
	}
	if _, err := session.Files().ReadFile(path.Join(stellaHomeMount, "bin", "host-only")); err == nil {
		t.Fatal("host binary tree impersonated the image-owned process view")
	}
	if got, err := session.Files().ReadFile(path.Join(sandboxpkg.MountBuiltinSkills, "SKILL.md")); err != nil || string(got) != "verified bundle" {
		t.Fatalf("verified builtin projection = %q, %v", got, err)
	}
}

func TestCreateSessionRequiresPrivateWorkspaceSource(t *testing.T) {
	api := &stopCountingAPI{}
	factory := &dockerFactory{
		cfg:      Config{Image: "test:latest", RuntimeMode: DockerSandboxModeHost},
		clientFn: func() (*dockerclient.Client, error) { return dockerclient.NewWithAPI(api), nil },
	}
	_, err := factory.CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: workspaceMount,
			Mounts:     []sandboxpkg.Mount{{SandboxPath: workspaceMount, Access: sandboxpkg.MountReadWrite}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `provider-private source for mount "/workspace" is required`) {
		t.Fatalf("CreateSession without private workspace source = %v", err)
	}
}

func TestCreateSessionDoesNotTreatWorkingDirAsPrivateWorkspaceSource(t *testing.T) {
	api := &stopCountingAPI{}
	factory := &dockerFactory{
		cfg:      Config{Image: "test:latest", RuntimeMode: DockerSandboxModeHost},
		clientFn: func() (*dockerclient.Client, error) { return dockerclient.NewWithAPI(api), nil },
	}
	_, err := factory.CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: "/host/workspace"},
	})
	if err == nil || !strings.Contains(err.Error(), "provider-private workspace source is required") {
		t.Fatalf("CreateSession inferred private source from WorkingDir: %v", err)
	}
}

func TestCreateSessionRequiresEveryPrivateMountSource(t *testing.T) {
	api := &stopCountingAPI{}
	factory := &dockerFactory{
		cfg:          Config{Image: "test:latest", RuntimeMode: DockerSandboxModeHost},
		mountSources: map[string]string{workspaceMount: t.TempDir()},
		clientFn:     func() (*dockerclient.Client, error) { return dockerclient.NewWithAPI(api), nil },
	}
	_, err := factory.CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: workspaceMount,
			Mounts: []sandboxpkg.Mount{
				{SandboxPath: workspaceMount, Access: sandboxpkg.MountReadWrite},
				{SandboxPath: userDataMount, Access: sandboxpkg.MountReadWrite},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `provider-private source for mount "/user" is required`) {
		t.Fatalf("CreateSession with ambient private mount source = %v", err)
	}
}

func TestCreateSessionRejectsOverlappingPhysicalMountsBeforeContainerCreate(t *testing.T) {
	api := &createCountingAPI{}
	workspace := t.TempDir()
	userData := filepath.Join(workspace, "nested-user")
	if err := os.Mkdir(userData, 0o700); err != nil {
		t.Fatal(err)
	}
	factory := &dockerFactory{
		cfg: Config{Image: "test:latest", RuntimeMode: DockerSandboxModeHost},
		mountSources: map[string]string{
			workspaceMount: workspace,
			userDataMount:  userData,
		},
		clientFn: func() (*dockerclient.Client, error) { return dockerclient.NewWithAPI(api), nil },
	}
	_, err := factory.CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: workspaceMount,
			Mounts: []sandboxpkg.Mount{
				{SandboxPath: workspaceMount, Access: sandboxpkg.MountReadWrite},
				{SandboxPath: userDataMount, Access: sandboxpkg.MountReadWrite},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "physical mount sources") {
		t.Fatalf("CreateSession with overlapping mount sources = %v", err)
	}
	if got := api.creates.Load(); got != 0 {
		t.Fatalf("container creates = %d, want 0", got)
	}
}

func TestCreateSessionStartFailureRemovesOwnedTemp(t *testing.T) {
	api := &startFailAPI{rootless: true}
	workspace := t.TempDir()
	factory := &dockerFactory{
		cfg:          Config{Image: "test:latest", Runtime: "runsc", RuntimeMode: DockerSandboxModeHost},
		mountSources: map[string]string{workspaceMount: workspace},
		clientFn:     func() (*dockerclient.Client, error) { return dockerclient.NewWithAPI(api), nil },
	}
	_, err := factory.CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: workspaceMount,
			Mounts:     []sandboxpkg.Mount{{SandboxPath: workspaceMount, Access: sandboxpkg.MountReadWrite}},
		},
	})
	if err == nil {
		t.Fatal("CreateSession succeeded despite container start failure")
	}
	var tempSource string
	for _, mount := range api.createOpts.HostConfig.Mounts {
		if mount.Target == "/tmp" {
			tempSource = mount.Source
			break
		}
	}
	if tempSource == "" {
		t.Fatal("CreateSession did not configure a /tmp mount")
	}
	if got, want := api.createOpts.Config.User, "0:0"; got != want {
		t.Errorf("rootless container user = %q, want %q", got, want)
	}
	if got := api.createOpts.HostConfig.Runtime; got != "runsc" {
		t.Errorf("container runtime = %q, want runsc", got)
	}
	if _, err := os.Stat(tempSource); !os.IsNotExist(err) {
		t.Fatalf("owned fallback temp survives start failure: %v", err)
	}
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

func TestCloseRemovesOwnedTempOnlyAfterSuccessfulStop(t *testing.T) {
	tmp := t.TempDir()
	api := &blockingStopAPI{stopStarted: make(chan struct{}), releaseStop: make(chan struct{})}
	s := &dockerSession{id: "session-1", client: dockerclient.NewWithAPI(api), containerID: "container-1", ownedTempDir: tmp, done: make(chan struct{})}
	closeErr := make(chan error, 1)
	go func() { closeErr <- s.Close() }()
	select {
	case <-api.stopStarted:
	case <-time.After(time.Second):
		t.Fatal("Close did not start container stop")
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("owned temp disappeared before stop completed: %v", err)
	}
	close(api.releaseStop)
	if err := <-closeErr; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("owned temp remains after successful stop: %v", err)
	}
}

func TestClosePreservesOwnedTempWhenStopFails(t *testing.T) {
	tmp := t.TempDir()
	stopErr := errors.New("stop failed")
	api := &blockingStopAPI{stopStarted: make(chan struct{}), releaseStop: make(chan struct{}), stopErr: stopErr}
	s := &dockerSession{id: "session-1", client: dockerclient.NewWithAPI(api), containerID: "container-1", ownedTempDir: tmp, done: make(chan struct{})}
	closeErr := make(chan error, 1)
	go func() { closeErr <- s.Close() }()
	<-api.stopStarted
	close(api.releaseStop)
	if err := <-closeErr; !errors.Is(err, stopErr) {
		t.Fatalf("Close error = %v, want %v", err, stopErr)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("owned temp was deleted after failed stop: %v", err)
	}
}

func TestCloseWinnerControlsDoneAndCloseErr(t *testing.T) {
	stopErr := errors.New("stop failed")
	api := &blockingStopAPI{stopStarted: make(chan struct{}), releaseStop: make(chan struct{}), stopErr: stopErr}
	s := &dockerSession{
		id:          "session-1",
		client:      dockerclient.NewWithAPI(api),
		containerID: "container-1",
		done:        make(chan struct{}),
	}

	firstCloseErr := make(chan error, 1)
	go func() { firstCloseErr <- s.Close() }()

	select {
	case <-api.stopStarted:
	case <-time.After(time.Second):
		t.Fatal("Close did not start container stop")
	}

	s.closeFromWatcher("container_exited", nil)
	select {
	case <-s.Done():
		t.Fatal("watcher loser closed done before Close assigned closeErr")
	default:
	}

	secondCloseErr := make(chan error, 1)
	go func() { secondCloseErr <- s.Close() }()
	select {
	case err := <-secondCloseErr:
		t.Fatalf("second Close returned before Stop completed: %v", err)
	default:
	}

	close(api.releaseStop)
	if err := <-firstCloseErr; !errors.Is(err, stopErr) {
		t.Fatalf("first Close error = %v, want %v", err, stopErr)
	}
	if err := <-secondCloseErr; !errors.Is(err, stopErr) {
		t.Fatalf("second Close error = %v, want %v", err, stopErr)
	}
}

func TestCloseFromWatcherPreservesOwnedTempWhenStopFails(t *testing.T) {
	tmp := t.TempDir()
	stopErr := errors.New("stop failed")
	api := &blockingStopAPI{stopStarted: make(chan struct{}), releaseStop: make(chan struct{}), stopErr: stopErr}
	s := &dockerSession{id: "session-1", client: dockerclient.NewWithAPI(api), containerID: "container-1", ownedTempDir: tmp, done: make(chan struct{})}
	closed := make(chan struct{})
	go func() {
		s.closeFromWatcher("container_liveness_error", errors.New("liveness failed"))
		close(closed)
	}()
	<-api.stopStarted
	close(api.releaseStop)
	<-closed
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("watcher removed owned temp after failed stop: %v", err)
	}
	if err := s.Close(); !errors.Is(err, stopErr) {
		t.Fatalf("Close after watcher error = %v, want %v", err, stopErr)
	}
}

func TestCloseFromWatcherReapsContainerAndExplicitCloseDoesNotDoubleRemove(t *testing.T) {
	tmp := t.TempDir()
	api := &stopCountingAPI{}
	s := &dockerSession{
		id:           "session-1",
		client:       dockerclient.NewWithAPI(api),
		containerID:  "container-1",
		ownedTempDir: tmp,
		done:         make(chan struct{}),
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
	if api.execs.Load() != 1 || api.execUser != "" {
		t.Fatalf("temp cleanup execs = %d, user = %q; want one exec as the image user", api.execs.Load(), api.execUser)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("watcher did not remove owned temp: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close after watcher close: %v", err)
	}
	if api.stops.Load() != 1 || api.removes.Load() != 1 {
		t.Fatalf("explicit Close double-cleaned: stop %d remove %d", api.stops.Load(), api.removes.Load())
	}
}
