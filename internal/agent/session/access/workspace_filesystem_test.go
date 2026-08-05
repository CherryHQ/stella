package access

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/fsops"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// workspaceFakeFilesystem is a provider-shaped fake: it only understands
// canonical /workspace and /user coordinates, so these tests cannot pass via a
// host-path fallback.
type workspaceFakeFilesystem struct {
	files           map[string][]byte
	dirs            map[string]bool
	err             error
	reads           []string
	renames         int
	renameErr       error
	unsorted        bool
	readInfo        *pkgsandbox.FileInfo
	readCloseErr    error
	ignoreReadLimit bool
}

type workspaceReadCloser struct {
	io.Reader
	err error
}

func (r workspaceReadCloser) Close() error { return r.err }

func newWorkspaceFakeFilesystem() *workspaceFakeFilesystem {
	return &workspaceFakeFilesystem{files: map[string][]byte{}, dirs: map[string]bool{"/workspace": true, "/user": true, "/user/assets": true}}
}

func (f *workspaceFakeFilesystem) Close() error { return nil }
func (f *workspaceFakeFilesystem) Read(_ context.Context, name string, options pkgsandbox.ReadOptions) (io.ReadCloser, pkgsandbox.FileInfo, error) {
	f.reads = append(f.reads, name)
	if f.err != nil {
		return nil, pkgsandbox.FileInfo{}, f.err
	}
	data, ok := f.files[name]
	if !ok {
		return nil, pkgsandbox.FileInfo{}, fs.ErrNotExist
	}
	if int64(len(data)) > options.MaxBytes && !f.ignoreReadLimit {
		return workspaceReadCloser{Reader: bytes.NewReader(data[:options.MaxBytes]), err: f.readCloseErr}, pkgsandbox.FileInfo{Name: path.Base(name), Size: int64(len(data))}, pkgsandbox.ErrReadLimit
	}
	info := pkgsandbox.FileInfo{Name: path.Base(name), Size: int64(len(data)), Mode: 0o644, ModTime: time.Unix(1, 0).UTC()}
	if f.readInfo != nil {
		info = *f.readInfo
	}
	return workspaceReadCloser{Reader: bytes.NewReader(data), err: f.readCloseErr}, info, nil
}

func (f *workspaceFakeFilesystem) Write(_ context.Context, name string, reader io.Reader, _ pkgsandbox.WriteOptions) error {
	if f.err != nil {
		return f.err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	f.files[name] = data
	return nil
}

func (f *workspaceFakeFilesystem) Upload(ctx context.Context, name string, reader io.Reader, options pkgsandbox.WriteOptions) error {
	return f.Write(ctx, name, reader, options)
}

func (f *workspaceFakeFilesystem) Stat(_ context.Context, name string) (pkgsandbox.FileInfo, error) {
	if f.err != nil {
		return pkgsandbox.FileInfo{}, f.err
	}
	if f.dirs[name] {
		return pkgsandbox.FileInfo{Name: path.Base(name), IsDir: true, Mode: fs.ModeDir}, nil
	}
	data, ok := f.files[name]
	if !ok {
		return pkgsandbox.FileInfo{}, fs.ErrNotExist
	}
	return pkgsandbox.FileInfo{Name: path.Base(name), Size: int64(len(data)), Mode: 0o644, ModTime: time.Unix(1, 0).UTC()}, nil
}

func (f *workspaceFakeFilesystem) List(_ context.Context, directory string) ([]pkgsandbox.DirEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	if !f.dirs[directory] {
		return nil, fs.ErrNotExist
	}
	entries := map[string]pkgsandbox.DirEntry{}
	for name, data := range f.files {
		rel, ok := strings.CutPrefix(name, directory+"/")
		if !ok || strings.Contains(rel, "/") {
			continue
		}
		entries[rel] = pkgsandbox.DirEntry{Name: rel, Size: int64(len(data))}
	}
	for name := range f.dirs {
		rel, ok := strings.CutPrefix(name, directory+"/")
		if !ok || strings.Contains(rel, "/") {
			continue
		}
		entries[rel] = pkgsandbox.DirEntry{Name: rel, IsDir: true}
	}
	out := make([]pkgsandbox.DirEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if f.unsorted {
		for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
			out[left], out[right] = out[right], out[left]
		}
	}
	return out, nil
}

func (f *workspaceFakeFilesystem) Mkdir(_ context.Context, name string, _ fs.FileMode) error {
	if f.err != nil {
		return f.err
	}
	for current := name; current != "/"; current = path.Dir(current) {
		f.dirs[current] = true
		if current == "/workspace" || current == "/user" {
			break
		}
	}
	return nil
}

func (f *workspaceFakeFilesystem) Remove(_ context.Context, name string, _ bool) error {
	if f.err != nil {
		return f.err
	}
	if _, ok := f.files[name]; ok {
		delete(f.files, name)
		return nil
	}
	if !f.dirs[name] {
		return fs.ErrNotExist
	}
	for candidate := range f.files {
		if candidate == name || strings.HasPrefix(candidate, name+"/") {
			delete(f.files, candidate)
		}
	}
	for candidate := range f.dirs {
		if candidate == name || strings.HasPrefix(candidate, name+"/") {
			delete(f.dirs, candidate)
		}
	}
	return nil
}

func (f *workspaceFakeFilesystem) Rename(_ context.Context, oldName, newName string) error {
	if f.err != nil {
		return f.err
	}
	f.renames++
	if f.renameErr != nil {
		return f.renameErr
	}
	if _, exists := f.files[newName]; exists || f.dirs[newName] {
		return fs.ErrExist
	}
	data, ok := f.files[oldName]
	if !ok {
		return fs.ErrNotExist
	}
	delete(f.files, oldName)
	f.files[newName] = data
	return nil
}

func TestWorkspaceOperationsUseOneExactFilesystemCallback(t *testing.T) {
	svc, runtime, _, authority := newRuntimeTestService(t)
	filesystem := newWorkspaceFakeFilesystem()
	runtime.filesystem = filesystem
	access, err := svc.Begin(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	operations := []struct {
		name string
		run  func() error
	}{
		{"list", func() error {
			_, err := access.ListWorkspace(context.Background(), WorkspaceListInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent})
			return err
		}},
		{"create", func() error {
			_, err := access.CreateWorkspacePath(context.Background(), WorkspaceCreateInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "new.txt", Content: "new"})
			return err
		}},
		{"delete", func() error {
			return access.DeleteWorkspacePath(context.Background(), WorkspacePathInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "new.txt"})
		}},
		{"move", func() error {
			filesystem.files["/workspace/old.txt"] = []byte("old")
			_, err := access.MoveWorkspacePath(context.Background(), WorkspaceMoveInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "old.txt", NewPath: "new.txt"})
			return err
		}},
		{"read", func() error {
			filesystem.files["/workspace/read.txt"] = []byte("read")
			_, err := access.ReadWorkspacePath(context.Background(), WorkspaceReadInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "read.txt"})
			return err
		}},
		{"write", func() error {
			_, err := access.WriteWorkspacePath(context.Background(), WorkspaceWriteInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "write.txt", Content: "write"})
			return err
		}},
		{"upload", func() error {
			_, err := access.UploadWorkspacePath(context.Background(), WorkspaceUploadInput{AgentID: "a1", SessionID: "s1", Filename: "upload.txt", Reader: strings.NewReader("upload"), Now: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)})
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			before := runtime.filesystemCalls
			if err := operation.run(); err != nil {
				t.Fatalf("%s: %v", operation.name, err)
			}
			if runtime.filesystemCalls != before+1 {
				t.Fatalf("callbacks = %d, want %d", runtime.filesystemCalls-before, 1)
			}
			if runtime.filesystemInfo.ID != "s1" || runtime.filesystemInfo.AgentID != "a1" {
				t.Fatalf("runtime got %+v, want exact authorized session", runtime.filesystemInfo)
			}
		})
	}
}

func TestWorkspaceRejectsForeignAndHostPathsBeforeCallback(t *testing.T) {
	svc, runtime, _, authority := newRuntimeTestService(t)
	runtime.filesystem = newWorkspaceFakeFilesystem()
	access, err := svc.Begin(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.ReadWorkspacePath(context.Background(), WorkspaceReadInput{AgentID: "a1", SessionID: "foreign", Scope: WorkspaceScopeAgent, Path: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign session error = %v", err)
	}
	if runtime.filesystemCalls != 0 {
		t.Fatalf("foreign session invoked %d callbacks", runtime.filesystemCalls)
	}
	if _, err := access.ReadWorkspacePath(context.Background(), WorkspaceReadInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "/private/stella/x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("host path error = %v", err)
	}
	if runtime.filesystemCalls != 0 {
		t.Fatalf("host path invoked %d callbacks", runtime.filesystemCalls)
	}
}

func TestWorkspaceSymlinkContainmentIsDelegatedToFilesystem(t *testing.T) {
	svc, runtime, _, authority := newRuntimeTestService(t)
	workspace, user := t.TempDir(), t.TempDir()
	if err := os.Symlink(t.TempDir(), path.Join(workspace, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	filesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: pkgsandbox.PathWorkspace, Directory: workspace}, {Path: pkgsandbox.PathUser, Directory: user}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = filesystem.Close() }()
	runtime.filesystem = filesystem
	access, err := svc.Begin(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	result, err := access.ReadWorkspacePath(context.Background(), WorkspaceReadInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "escape/secret.txt"})
	if err == nil || result.Content != "" {
		t.Fatalf("escaping symlink read = %#v, %v; want Filesystem rejection", result, err)
	}
}

func TestWorkspaceAliasProviderErrorsAndUploadReadRoundTrip(t *testing.T) {
	svc, runtime, _, authority := newRuntimeTestService(t)
	filesystem := newWorkspaceFakeFilesystem()
	runtime.filesystem = filesystem
	access, err := svc.Begin(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	filesystem.files["/user/assets/202608/a.txt"] = []byte("alias")
	result, err := access.ReadWorkspacePath(context.Background(), WorkspaceReadInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "$STELLA_ASSETS_DIR/202608/a.txt"})
	if err != nil || result.Content != "alias" || len(filesystem.reads) != 1 || filesystem.reads[0] != "/user/assets/202608/a.txt" {
		t.Fatalf("alias read = %#v, %v; paths=%v", result, err, filesystem.reads)
	}
	providerErr := errors.New("provider failed")
	filesystem.err = providerErr
	if _, err := access.ListWorkspace(context.Background(), WorkspaceListInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeUser}); !errors.Is(err, providerErr) {
		t.Fatalf("provider error = %v", err)
	}
	filesystem.err = nil
	upload, err := access.UploadWorkspacePath(context.Background(), WorkspaceUploadInput{AgentID: "a1", SessionID: "s1", Filename: "roundtrip.txt", Reader: strings.NewReader("roundtrip"), Now: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)})
	if err != nil || upload.Path == "" || strings.Contains(upload.Path, "STELLA_HOME") || upload.RelativePath == "" {
		t.Fatalf("upload = %#v, %v", upload, err)
	}
	read, err := access.ReadWorkspacePath(context.Background(), WorkspaceReadInput{AgentID: "a1", SessionID: "s1", Scope: upload.Scope, Path: upload.RelativePath})
	if err != nil || read.Content != "roundtrip" {
		t.Fatalf("roundtrip = %#v, %v", read, err)
	}
}
