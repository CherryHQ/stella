package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/fsops/fstest"
	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

// TestDockerFilesystemConformanceRealImage runs the provider-neutral suite
// (shared with library/local/none) against a real container built from the
// supplied sandbox image, exercising the stella-fs helper end to end over the
// exec transport. It is opt-in: ordinary `go test` never pulls an image or
// touches an operator home, so it skips precisely when the image env is unset.
func TestDockerFilesystemConformanceRealImage(t *testing.T) {
	image := os.Getenv("STELLA_TEST_SANDBOX_IMAGE")
	if image == "" {
		t.Skip("STELLA_TEST_SANDBOX_IMAGE is unset; build the sandbox image and rerun this capability test")
	}
	ctx := context.Background()

	// Only temporary directories: a private STELLA_HOME, workspace, and a
	// read-only user root. Nothing touches the developer's real operator home.
	stellaHome := tempAbsDir(t, "stella-home")
	workspace := tempAbsDir(t, "workspace")
	readOnly := tempAbsDir(t, "user")

	factory, err := NewFactory(Config{Image: image, StellaHome: stellaHome, RuntimeMode: DockerSandboxModeHost})
	if err != nil {
		t.Fatal(err)
	}
	if !factory.Available() {
		t.Skip("Docker daemon unavailable for STELLA_TEST_SANDBOX_IMAGE conformance")
	}

	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: workspace,
			Mounts: []sandboxpkg.Mount{
				{HostPath: workspace, SandboxPath: sandboxpkg.PathWorkspace, Access: sandboxpkg.MountReadWrite},
				{HostPath: readOnly, SandboxPath: sandboxpkg.PathUser, Access: sandboxpkg.MountReadOnly},
			},
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkDisabled},
		Timeout: 2 * time.Minute,
	}
	session, err := factory.CreateSession(ctx, policy)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Deterministic teardown: reap the container before the temp dirs vanish.
	t.Cleanup(func() { _ = session.Close() })

	withFS, ok := session.(sandboxpkg.FilesystemSession)
	if !ok {
		t.Fatal("docker session does not expose Filesystem")
	}
	filesystem, err := withFS.Filesystem()
	if err != nil {
		t.Fatalf("Filesystem: %v", err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })

	fstest.Run(t, fstest.Harness{
		FS:           filesystem,
		ReadOnlyPath: "/user/blocked",
		InjectSymlink: func(name, target string) error {
			// Plant the symlink with in-container tooling; the suite chooses whether
			// the target escapes (must fail closed) or stays contained (must resolve).
			cmd := fmt.Sprintf("ln -s %s %s", target, filepath.Join(sandboxpkg.PathWorkspace, filepath.FromSlash(name)))
			res, err := session.Exec(ctx, cmd, sandboxpkg.ExecOptions{})
			if err != nil {
				return err
			}
			if res.ExitCode != 0 {
				return fmt.Errorf("ln exited %d: %s", res.ExitCode, res.Stderr)
			}
			return nil
		},
	})
}

func tempAbsDir(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "stella-fs-real-"+name+"-")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	return abs
}
