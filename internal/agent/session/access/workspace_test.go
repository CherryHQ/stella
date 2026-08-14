package access

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/home"
)

// testWorkspaceViewer provides a rooted Home fixture for workspace tests.
type testWorkspaceViewer struct{}

func (testWorkspaceViewer) WorkspaceView(_ context.Context, req home.WorkspaceRequest) (home.WorkspaceView, error) {
	principal := filepath.Join(config.StellaHome(), "users", req.UserID)
	if req.GroupID != "" {
		principal = filepath.Join(config.StellaHome(), "users", "group-"+req.GroupID)
	}
	agent := filepath.Join(principal, "agents", req.AgentID)
	for _, dir := range []string{filepath.Join(principal, "data", "assets"), agent} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return home.WorkspaceView{}, err
		}
	}
	return home.WorkspaceView{PrincipalRoot: principal, DataRoot: filepath.Join(principal, "data"), AgentRoot: agent}, nil
}

func (testWorkspaceViewer) OpenRoot(_ context.Context, req home.WorkspaceRequest, scope home.RootScope, _ home.RootAccess) (home.RootOperations, error) {
	view, err := testWorkspaceViewer{}.WorkspaceView(context.Background(), req)
	if err != nil {
		return nil, err
	}
	dir := view.AgentRoot
	if scope == home.RootPrincipalData {
		dir = view.DataRoot
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return testRootOperations{Root: r}, nil
}

type testRootOperations struct{ *os.Root }

type closeFailingWorkspace struct {
	testWorkspaceViewer
	err error
}

func (w closeFailingWorkspace) OpenRoot(ctx context.Context, req home.WorkspaceRequest, scope home.RootScope, access home.RootAccess) (home.RootOperations, error) {
	root, err := w.testWorkspaceViewer.OpenRoot(ctx, req, scope, access)
	if err != nil {
		return nil, err
	}
	return closeFailingRoot{RootOperations: root, err: w.err}, nil
}

type closeFailingRoot struct {
	home.RootOperations
	err error
}

func (r closeFailingRoot) Close() error { return errors.Join(r.RootOperations.Close(), r.err) }

func (r testRootOperations) Close() error { return r.Root.Close() }
func (r testRootOperations) Stat(_ context.Context, name string) (fs.FileInfo, error) {
	return r.Root.Stat(name)
}

func (r testRootOperations) List(_ context.Context, name string, o home.ListOptions) ([]fs.DirEntry, error) {
	d, err := r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	f, err := d.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ReadDir(o.Limit)
}

func (r testRootOperations) Read(_ context.Context, name string, dst io.Writer, o home.ReadOptions) error {
	f, err := r.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(dst, io.LimitReader(f, o.MaxBytes))
	return err
}

func (r testRootOperations) Write(_ context.Context, name string, src io.Reader, o home.WriteOptions) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	return r.WriteFile(name, data, o.Mode)
}

func (r testRootOperations) Upload(ctx context.Context, name string, src io.Reader, o home.WriteOptions) error {
	return r.Write(ctx, name, src, o)
}

func (r testRootOperations) Mkdir(_ context.Context, name string, mode fs.FileMode, o home.MkdirOptions) error {
	if o.Parents {
		return r.MkdirAll(name, mode)
	}
	return r.Root.Mkdir(name, mode)
}

func (r testRootOperations) Remove(_ context.Context, name string, o home.RemoveOptions) error {
	if o.Recursive {
		return r.RemoveAll(name)
	}
	return r.Root.Remove(name)
}

func (r testRootOperations) Rename(_ context.Context, old, new string, _ home.RenameOptions) error {
	return r.Root.Rename(old, new)
}

func workspaceRootForScope(userID, agentID string, scope WorkspaceScope) string {
	view, _ := testWorkspaceViewer{}.WorkspaceView(context.Background(), home.WorkspaceRequest{UserID: userID, AgentID: agentID})
	if scope == WorkspaceScopeUser {
		return view.DataRoot
	}
	return view.AgentRoot
}
