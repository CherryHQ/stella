package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"
)

type stopCountingAPI struct {
	noopAPI
	stops      atomic.Int32
	removes    atomic.Int32
	execs      atomic.Int32
	execUser   string
	createOpts mobyclient.ContainerCreateOptions
}

func (f *stopCountingAPI) ContainerCreate(_ context.Context, opts mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error) {
	f.createOpts = opts
	return mobyclient.ContainerCreateResult{}, nil
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
}

func (f *startFailAPI) ContainerCreate(_ context.Context, opts mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error) {
	f.createOpts = opts
	return mobyclient.ContainerCreateResult{ID: "container-1"}, nil
}

func (startFailAPI) ContainerStart(context.Context, string, mobyclient.ContainerStartOptions) (mobyclient.ContainerStartResult, error) {
	return mobyclient.ContainerStartResult{}, errors.New("start failed")
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
	project := filepath.Join(workspace, "projects", "p")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	factory := &dockerFactory{
		cfg: Config{Image: "test:latest", RuntimeMode: DockerSandboxModeHost, Layout: hostlayout.Layout{
			WorkspaceSource: workspace, WorkingDirSource: project,
			Mounts: []hostlayout.Mount{{Source: workspace, Target: workspaceMount, Access: hostlayout.ReadWrite}},
		}},
		clientFn: func() (*dockerclient.Client, error) { return dockerclient.NewWithAPI(api), nil },
	}
	session, err := factory.CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: filepath.Join(t.TempDir(), "redirected-workspace"),
			WorkingDir:    filepath.Join(t.TempDir(), "redirected-working-dir"),
			Mounts:        []sandboxpkg.Mount{{HostPath: filepath.Join(t.TempDir(), "redirected-mount"), SandboxPath: "/redirected", Access: sandboxpkg.MountReadWrite}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	policy := session.Policy()
	if got, want := session.WorkingDir(), "/workspace/projects/p"; got != want {
		t.Errorf("WorkingDir() = %q, want %q", got, want)
	}
	if policy.Filesystem.WorkspaceRoot != "" || len(policy.Filesystem.Mounts) != 0 {
		t.Errorf("Policy retained physical layout: %#v", policy.Filesystem)
	}
	if got, want := policy.Filesystem.WorkingDir, "/workspace/projects/p"; got != want {
		t.Errorf("policy working directory = %q, want %q", got, want)
	}
	for _, mount := range api.createOpts.HostConfig.Mounts {
		if mount.Target == workspaceMount && mount.Source != workspace {
			t.Errorf("workspace mount source = %q, want Layout source %q", mount.Source, workspace)
		}
	}
	if got := policy.Env[sandboxpkg.EnvTempDir]; got != "/tmp" {
		t.Fatalf("policy TMPDIR = %q, want container coordinate /tmp", got)
	}
	tempDir := session.(*dockerSession).ownedTempDir
	if tempDir == "" {
		t.Fatal("policy TMPDIR is empty, want a private session directory")
	}
	if got, err := session.(*dockerSession).host.resolveWritePath("/tmp/file"); err != nil || got != filepath.Join(tempDir, "file") {
		t.Errorf("ResolveWritePath(/tmp/file) = %q, %v; want %q, nil", got, err, filepath.Join(tempDir, "file"))
	}
}

func TestCreateSessionRejectsUnmappableWorkingDirBeforeContainerStart(t *testing.T) {
	api := &startFailAPI{}
	workspace := t.TempDir()
	factory := &dockerFactory{
		cfg: Config{Image: "test:latest", RuntimeMode: DockerSandboxModeHost, Layout: hostlayout.Layout{
			WorkspaceSource: workspace, WorkingDirSource: filepath.Join(t.TempDir(), "outside-workspace"),
			Mounts: []hostlayout.Mount{{Source: workspace, Target: workspaceMount, Access: hostlayout.ReadWrite}},
		}},
		clientFn: func() (*dockerclient.Client, error) { return dockerclient.NewWithAPI(api), nil },
	}
	_, err := factory.CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: workspace,
			WorkingDir:    filepath.Join(t.TempDir(), "outside-workspace"),
		},
	})
	if err == nil {
		t.Fatal("CreateSession accepted an unmappable working directory")
	}
	if api.createOpts.Name != "" {
		t.Fatalf("CreateSession reached ContainerCreate for an unmappable working directory: %+v", api.createOpts)
	}
}

func TestCreateSessionStartFailureRemovesOwnedTemp(t *testing.T) {
	api := &startFailAPI{}
	workspace := t.TempDir()
	factory := &dockerFactory{
		cfg: Config{Image: "test:latest", RuntimeMode: DockerSandboxModeHost, Layout: hostlayout.Layout{
			WorkspaceSource: workspace, WorkingDirSource: workspace,
			Mounts: []hostlayout.Mount{{Source: workspace, Target: workspaceMount, Access: hostlayout.ReadWrite}},
		}},
		clientFn: func() (*dockerclient.Client, error) { return dockerclient.NewWithAPI(api), nil },
	}
	_, err := factory.CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: workspace,
			WorkingDir:    workspace,
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
	if got, want := api.createOpts.Config.User, dockerProcessUser(); got != want {
		t.Errorf("container user = %q, want stellad process user %q", got, want)
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
