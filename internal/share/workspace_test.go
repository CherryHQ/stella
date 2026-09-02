package share_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/home"
)

type allowAgentRead struct{}

func (allowAgentRead) Read(_ context.Context, _ authz.Authority, agentID string) (config.Agent, error) {
	return config.Agent{ID: agentID}, nil
}

// testWorkspaceViewer is explicit test wiring, never a production fallback.
type testWorkspaceViewer struct{ root string }

func (w testWorkspaceViewer) WorkspaceView(_ context.Context, req home.WorkspaceRequest) (home.WorkspaceView, error) {
	principal := filepath.Join(w.root, "users", req.UserID)
	if req.GroupID != "" {
		principal = filepath.Join(w.root, "users", "group-"+req.GroupID)
	}
	data, agent := filepath.Join(principal, "data"), filepath.Join(principal, "agents", req.AgentID)
	if err := os.MkdirAll(filepath.Join(data, "assets"), 0o755); err != nil {
		return home.WorkspaceView{}, err
	}
	if err := os.MkdirAll(agent, 0o755); err != nil {
		return home.WorkspaceView{}, err
	}
	return home.WorkspaceView{PrincipalRoot: principal, DataRoot: data, AgentRoot: agent}, nil
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
	return testRoot{Root: root}, nil
}

type testRoot struct{ *os.Root }

func (r testRoot) Close() error { return r.Root.Close() }
func (r testRoot) Stat(_ context.Context, name string) (fs.FileInfo, error) {
	return r.Root.Stat(name)
}

func (r testRoot) List(_ context.Context, name string, options home.ListOptions) ([]fs.DirEntry, error) {
	dir, err := r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	f, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ReadDir(options.Limit)
}

func (r testRoot) Read(_ context.Context, name string, dst io.Writer, options home.ReadOptions) error {
	f, err := r.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	written, err := io.Copy(dst, io.LimitReader(f, options.MaxBytes))
	if err != nil {
		return err
	}
	if written == options.MaxBytes {
		var extra [1]byte
		if n, readErr := f.Read(extra[:]); n != 0 {
			return home.ErrReadLimit
		} else if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
	}
	return nil
}

func (r testRoot) Write(_ context.Context, name string, src io.Reader, options home.WriteOptions) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	return r.WriteFile(name, data, options.Mode)
}

func (r testRoot) Upload(ctx context.Context, name string, src io.Reader, options home.WriteOptions) error {
	return r.Write(ctx, name, src, options)
}

func (r testRoot) Mkdir(_ context.Context, name string, mode fs.FileMode, options home.MkdirOptions) error {
	if options.Parents {
		return r.MkdirAll(name, mode)
	}
	return r.Root.Mkdir(name, mode)
}

func (r testRoot) Remove(_ context.Context, name string, options home.RemoveOptions) error {
	if options.Recursive {
		return r.RemoveAll(name)
	}
	return r.Root.Remove(name)
}

func (r testRoot) Rename(_ context.Context, oldName, newName string, _ home.RenameOptions) error {
	return r.Root.Rename(oldName, newName)
}
