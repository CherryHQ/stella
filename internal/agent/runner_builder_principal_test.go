package agent

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type testWorkspaceViewer struct{ root string }

type failingWorkspaceViewer struct{ err error }

func (w failingWorkspaceViewer) WorkspaceView(context.Context, home.WorkspaceRequest) (home.WorkspaceView, error) {
	return home.WorkspaceView{}, w.err
}

func (w testWorkspaceViewer) WorkspaceView(_ context.Context, req home.WorkspaceRequest) (home.WorkspaceView, error) {
	shared := home.WorkspaceView{}
	if req.GroupID != "" {
		principal := GroupHomeDir(w.root, req.GroupID)
		agent := AgentDirInHome(principal, req.AgentID)
		if err := os.MkdirAll(agent, 0o755); err != nil {
			return home.WorkspaceView{}, err
		}
		shared.PrincipalRoot, shared.DataRoot, shared.AgentRoot = principal, filepath.Join(principal, "data"), agent
		return shared, nil
	}
	if req.UserID != "" {
		principal := UserHomeDir(w.root, req.UserID)
		agent := AgentDirInHome(principal, req.AgentID)
		if err := os.MkdirAll(agent, 0o755); err != nil {
			return home.WorkspaceView{}, err
		}
		shared.PrincipalRoot, shared.DataRoot, shared.AgentRoot = principal, filepath.Join(principal, "data"), agent
		return shared, nil
	}
	return shared, nil
}

func (w testWorkspaceViewer) ResolveCoordinate(c home.Coordinate) (home.RootScope, string, error) {
	if scope, name, err := home.ResolveLogicalCoordinate(c.Scope, c.Value, c.AllowRoot); err == nil {
		return scope, name, nil
	}
	view, err := w.WorkspaceView(context.Background(), c.Request)
	if err != nil {
		return 0, "", err
	}
	for _, candidate := range []struct {
		scope home.RootScope
		root  string
	}{{home.RootAgentWorkspace, view.AgentRoot}, {home.RootPrincipalData, view.DataRoot}} {
		rel, err := filepath.Rel(candidate.root, c.Value)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return home.ResolveLogicalCoordinate(candidate.scope, filepath.ToSlash(rel), c.AllowRoot)
		}
	}
	return 0, "", errors.New("test coordinate escapes workspace")
}

func (w testWorkspaceViewer) OpenRoot(ctx context.Context, req home.WorkspaceRequest, scope home.RootScope, _ home.RootAccess) (home.RootOperations, error) {
	view, err := w.WorkspaceView(ctx, req)
	if err != nil {
		return nil, err
	}
	dir := view.AgentRoot
	if scope == home.RootPrincipalData {
		dir = view.DataRoot
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return runnerTestRoot{Root: root}, nil
}

type runnerTestRoot struct{ *os.Root }

func (r runnerTestRoot) Stat(_ context.Context, name string) (fs.FileInfo, error) {
	return r.Root.Stat(name)
}

func (r runnerTestRoot) List(_ context.Context, name string, options home.ListOptions) ([]fs.DirEntry, error) {
	directory, err := r.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(options.Limit + 1)
	if err != nil {
		return nil, err
	}
	if options.Limit > 0 && len(entries) > options.Limit {
		return nil, home.ErrListLimit
	}
	return entries, nil
}

func (r runnerTestRoot) Read(_ context.Context, name string, dst io.Writer, options home.ReadOptions) error {
	file, err := r.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = io.Copy(dst, io.LimitReader(file, options.MaxBytes))
	return err
}

func (r runnerTestRoot) Write(context.Context, string, io.Reader, home.WriteOptions) error {
	return errors.New("not implemented")
}

func (r runnerTestRoot) Upload(context.Context, string, io.Reader, home.WriteOptions) error {
	return errors.New("not implemented")
}

func (r runnerTestRoot) Mkdir(context.Context, string, fs.FileMode, home.MkdirOptions) error {
	return errors.New("not implemented")
}

func (r runnerTestRoot) Remove(context.Context, string, home.RemoveOptions) error {
	return errors.New("not implemented")
}

func (r runnerTestRoot) Rename(context.Context, string, string, home.RenameOptions) error {
	return errors.New("not implemented")
}

func TestNewRunnerFuncUsesPrincipalWorkspace(t *testing.T) {
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	snap := &config.Snapshot{AgentID: "a1", Provider: "anthropic", Model: "test-model", APIKey: "test-key"}
	snap.Workspace = t.TempDir()
	for _, tt := range []struct {
		name     string
		params   RunnerParams
		wantRoot string
		wantWork string
	}{
		{name: "personal", params: RunnerParams{UserID: "u1", AgentID: "a1"}, wantRoot: UserHomeDir(stellaHome, "u1"), wantWork: filepath.Join(stellaHome, "users", "u1", "agents", "a1")},
		{name: "group", params: RunnerParams{UserID: "g1", GroupID: "g1", AgentID: "a1"}, wantRoot: GroupHomeDir(stellaHome, "g1"), wantWork: filepath.Join(stellaHome, "users", "group-g1", "agents", "a1")},
		{name: "user-less", params: RunnerParams{AgentID: "a1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var promptBuild plugins.SystemPromptContext
			build := newRunnerFunc(runnerBuilderConfig{
				Snap:            snap,
				WorkspaceViewer: testWorkspaceViewer{root: stellaHome},
				PromptSectionsBuilder: func(_ context.Context, build plugins.SystemPromptContext) ([]plugins.SystemPromptSection, error) {
					promptBuild = build
					return nil, nil
				},
				ProviderStreamBuilder: func(_, _, _ string) (providers.StreamFunc, error) {
					return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
				},
				SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
			})
			builtRunner, err := build(context.Background(), tt.params)
			if err != nil {
				t.Fatalf("build runner: %v", err)
			}
			t.Cleanup(func() { _ = builtRunner.Close() })
			if tt.name == "user-less" {
				if promptBuild.UserRoot == "" || filepath.Dir(promptBuild.UserRoot) != filepath.Join(stellaHome, runnerScratchDir) {
					t.Errorf("user-less root = %q, want disposable scratch", promptBuild.UserRoot)
				}
				for _, dir := range []string{filepath.Dir(promptBuild.UserRoot), promptBuild.UserRoot} {
					info, err := os.Stat(dir)
					if err != nil || info.Mode().Perm() != 0o700 {
						t.Fatalf("scratch permissions for %q = %v, %v; want 0700", dir, info, err)
					}
				}
				if err := os.WriteFile(filepath.Join(promptBuild.UserRoot, "owned"), []byte("ok"), 0o600); err != nil {
					t.Fatalf("scratch is not writable: %v", err)
				}
				impl := builtRunner.(*runner)
				workspaceRoot, err := filepath.EvalSymlinks(impl.session.Policy().Filesystem.WorkspaceRoot)
				promptRoot, promptErr := filepath.EvalSymlinks(promptBuild.UserRoot)
				if err != nil || promptErr != nil || impl.sandboxCfg.Paths.AgentRoot != snap.Workspace || workspaceRoot != promptRoot {
					t.Fatalf("definition/scratch roots = agent %q workspace %q scratch %q", impl.sandboxCfg.Paths.AgentRoot, impl.session.Policy().Filesystem.WorkspaceRoot, promptBuild.UserRoot)
				}
				scratch := promptBuild.UserRoot
				if err := builtRunner.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(scratch); !os.IsNotExist(err) {
					t.Fatalf("scratch remains after Close: %v", err)
				}
			} else {
				if got := promptBuild.UserRoot; got != tt.wantRoot {
					t.Errorf("prompt UserRoot = %q, want %q", got, tt.wantRoot)
				}
				if got := promptBuild.WorkspaceRoot; got != tt.wantWork {
					t.Errorf("prompt WorkspaceRoot = %q, want %q", got, tt.wantWork)
				}
				mounts := builtRunner.(*runner).session.Policy().Filesystem.Mounts
				wantMounts := map[string]string{
					pkgsandbox.MountWorkspace: tt.wantWork,
					pkgsandbox.MountUserData:  filepath.Join(tt.wantRoot, "data"),
				}
				for sandboxPath, hostPath := range wantMounts {
					found := false
					for _, mount := range mounts {
						if mount.SandboxPath == sandboxPath {
							found = mount.HostPath == hostPath && mount.Access == pkgsandbox.MountReadWrite
						}
					}
					if !found {
						t.Errorf("mount %s = %#v, want RW host %q", sandboxPath, mounts, hostPath)
					}
				}
			}
		})
	}
}

func TestNewRunnerFuncPropagatesWorkspaceError(t *testing.T) {
	want := errors.New("Home unavailable")
	build := newRunnerFunc(runnerBuilderConfig{
		Snap:            &config.Snapshot{Provider: "anthropic", Model: "test"},
		WorkspaceViewer: failingWorkspaceViewer{err: want},
	})
	if _, err := build(context.Background(), RunnerParams{UserID: "u", AgentID: "a"}); !errors.Is(err, want) {
		t.Fatalf("runner error = %v, want %v", err, want)
	}
}

func TestNewRunnerFuncRejectsUserlessProject(t *testing.T) {
	build := newRunnerFunc(runnerBuilderConfig{
		Snap:            &config.Snapshot{Provider: "anthropic", Model: "test"},
		WorkspaceViewer: testWorkspaceViewer{root: t.TempDir()},
	})
	if _, err := build(context.Background(), RunnerParams{AgentID: "a", ProjectID: "p"}); err == nil {
		t.Fatal("user-less ProjectID was accepted")
	}
}

func TestNewRunnerFuncCleansUserlessScratchOnConstructionFailure(t *testing.T) {
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	var scratch string
	build := newRunnerFunc(runnerBuilderConfig{
		Snap:            &config.Snapshot{AgentID: "a", Provider: "anthropic", Model: "test"},
		WorkspaceViewer: testWorkspaceViewer{root: stellaHome},
		PromptSectionsBuilder: func(_ context.Context, build plugins.SystemPromptContext) ([]plugins.SystemPromptSection, error) {
			scratch = build.UserRoot
			return nil, nil
		},
		ProviderStreamBuilder: func(_, _, _ string) (providers.StreamFunc, error) {
			return nil, errors.New("provider unavailable")
		},
		SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
	})
	if _, err := build(context.Background(), RunnerParams{AgentID: "a"}); err == nil {
		t.Fatal("runner construction succeeded")
	}
	if scratch == "" {
		t.Fatal("scratch was not created before construction failure")
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch remains after construction failure: %v", err)
	}
}

func TestNewRunnerScratchCleanupUsesOpenedRootAfterPathReplacement(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" || runtime.GOOS == "js" {
		t.Skip("open directory handles do not remain usable across rename on this platform")
	}
	home := t.TempDir()
	dir, cleanup, err := newRunnerScratch(home)
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := filepath.Join(home, "old-runner-scratch")
	if err := os.Rename(filepath.Join(home, runnerScratchDir), oldRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, runnerScratchDir), 0o700); err != nil {
		t.Fatal(err)
	}
	replacementMarker := filepath.Join(home, runnerScratchDir, "keep")
	if err := os.WriteFile(replacementMarker, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(oldRoot, filepath.Base(dir))); !os.IsNotExist(err) {
		t.Fatalf("scratch remains in original opened root: %v", err)
	}
	if _, err := os.Stat(replacementMarker); err != nil {
		t.Fatalf("replacement root was modified: %v", err)
	}
}
