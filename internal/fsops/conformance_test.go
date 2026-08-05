package fsops_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/fsops"
	"github.com/CherryHQ/stella/internal/fsops/fstest"
	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/local"
	"github.com/CherryHQ/stella/plugins/sandbox/none"
)

// TestFilesystemConformance runs the single provider-neutral suite against every
// in-process provider. The Docker adapter runs the same suite from its own
// package (filesystem_real_test.go) against a real container image.
func TestFilesystemConformance(t *testing.T) {
	t.Run("library", func(t *testing.T) {
		fstest.Run(t, libraryHarness(t))
	})
	t.Run("none", func(t *testing.T) {
		fstest.Run(t, sessionHarness(t, none.NewFactory()))
	})
	t.Run("local", func(t *testing.T) {
		fstest.Run(t, sessionHarness(t, local.NewFactory()))
	})
}

// libraryHarness exercises the raw fsops.Filesystem with a read-write workspace
// and a read-only user mount. Symlinks are planted host-side, the only place a
// host path is touched — never inside the suite.
func libraryHarness(t *testing.T) fstest.Harness {
	t.Helper()
	workspace, readOnly := t.TempDir(), t.TempDir()
	fs, err := fsops.NewFilesystem([]fsops.Mount{
		{Path: sandboxpkg.PathWorkspace, Directory: workspace},
		{Path: sandboxpkg.PathUser, Directory: readOnly, ReadOnly: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fstest.Harness{
		FS:            fs,
		ReadOnlyPath:  "/user/blocked",
		InjectSymlink: hostSymlink(workspace),
	}
}

// sessionHarness drives a real backend session's mediated Filesystem.
func sessionHarness(t *testing.T, factory sandboxpkg.Factory) fstest.Harness {
	t.Helper()
	workspace, readOnly := t.TempDir(), t.TempDir()
	session, err := factory.CreateSession(context.Background(), sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{
		WorkingDir: workspace,
		Mounts: []sandboxpkg.Mount{
			{HostPath: workspace, SandboxPath: sandboxpkg.PathWorkspace, Access: sandboxpkg.MountReadWrite},
			{HostPath: readOnly, SandboxPath: sandboxpkg.PathUser, Access: sandboxpkg.MountReadOnly},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	withFS, ok := session.(sandboxpkg.FilesystemSession)
	if !ok {
		t.Fatal("session has no filesystem")
	}
	fs, err := withFS.Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fstest.Harness{
		FS:            fs,
		ReadOnlyPath:  "/user/blocked",
		InjectSymlink: hostSymlink(workspace),
	}
}

// hostSymlink returns an injector that plants a symlink (with the target the
// suite chooses) under the backing directory. It is the only host-path
// operation, kept out of the suite.
func hostSymlink(root string) func(name, target string) error {
	return func(name, target string) error {
		return os.Symlink(target, filepath.Join(root, filepath.FromSlash(name)))
	}
}
