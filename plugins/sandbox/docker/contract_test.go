package docker_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dockerplugin "github.com/CherryHQ/stella/plugins/sandbox/docker"
	dockerclient "github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// dockerAvailable probes whether the docker daemon is reachable.
func dockerAvailable(ctx context.Context) bool {
	client, err := dockerclient.New()
	if err != nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = client.Version(probeCtx)
	return err == nil
}

const dockerContractImage = "alpine:3.20"

func dockerPreflightForTest(t *testing.T, ctx context.Context) {
	t.Helper()
	cfg := dockerplugin.PreflightConfig{Docker: dockerplugin.Config{Image: dockerContractImage}}
	preflightCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := dockerplugin.Preflight(preflightCtx, cfg); err != nil {
		t.Skipf("docker preflight failed (image unavailable): %v", err)
	}
}

func TestSessionContract(t *testing.T) {
	t.Run("DockerFactory", func(t *testing.T) {
		ctx := context.Background()
		if !dockerAvailable(ctx) {
			t.Skip("docker daemon not reachable; skipping DockerFactory contract test")
		}
		dockerPreflightForTest(t, ctx)
		stellaHome, err := os.MkdirTemp(".", "docker-contract-stella-home-")
		if err != nil {
			t.Fatal(err)
		}
		stellaHome, err = filepath.Abs(stellaHome)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(stellaHome) })
		testSessionContract(t, func(layout hostlayout.Layout) (sandbox.Factory, error) {
			return dockerplugin.NewFactory(dockerplugin.Config{Image: dockerContractImage, StellaHome: stellaHome, RuntimeMode: dockerplugin.DockerSandboxModeHost, Layout: layout})
		})
	})
}

func testSessionContract(t *testing.T, newFactory func(hostlayout.Layout) (sandbox.Factory, error)) {
	ctx := context.Background()
	workspace, err := os.MkdirTemp(".", "docker-contract-workspace-")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	project := filepath.Join(workspace, "projects", "p")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	factory, err := newFactory(hostlayout.Layout{WorkspaceSource: workspace, WorkingDirSource: project, Mounts: []hostlayout.Mount{{Source: workspace, Target: sandbox.MountWorkspace, Access: hostlayout.ReadWrite}}})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	policy := sandbox.Policy{
		Filesystem: sandbox.FilesystemPolicy{
			WorkspaceRoot: workspace,
			WorkingDir:    project,
		},
		Network: sandbox.NetworkPolicy{
			Mode: sandbox.NetworkDisabled,
		},
		Timeout: 10 * time.Second,
	}

	t.Run("CreateAndClose", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		if !session.Alive() {
			t.Error("session should be alive after creation")
		}

		if session == nil {
			t.Error("session should be non-nil")
		}

		if err := session.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}

		if session.Alive() {
			t.Error("session should not be alive after Close()")
		}
	})

	t.Run("DoneChannel", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		done := session.Done()
		select {
		case <-done:
			t.Error("Done() should not be closed before Close()")
		case <-time.After(50 * time.Millisecond):
			// Expected
		}

		if err := session.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}

		select {
		case <-done:
			// Expected
		case <-time.After(time.Second):
			t.Error("Done() should be closed after Close()")
		}
	})

	t.Run("DoubleCloseIsSafe", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		if err := session.Close(); err != nil {
			t.Errorf("first Close: %v", err)
		}

		if err := session.Close(); err != nil {
			t.Logf("second Close returned error (acceptable): %v", err)
		}
	})

	t.Run("HostConsistency", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		if got := session.WorkingDir(); got != "/workspace/projects/p" {
			t.Errorf("WorkingDir() = %q, want normalized project directory %q", got, "/workspace/projects/p")
		}

		got, err := session.Exec(ctx, "pwd", sandbox.ExecOptions{})
		if err != nil || got.ExitCode != 0 {
			t.Fatalf("Exec(pwd) = %+v, %v", got, err)
		}
		if pwd := strings.TrimSuffix(got.Stdout, "\n"); pwd != session.WorkingDir() {
			t.Errorf("Exec(pwd) = %q, want normalized working directory %q", pwd, session.WorkingDir())
		}
	})

	t.Run("FilesystemAndExecShareCanonicalMounts", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		// alpine is sufficient for the generic Session contract but does not
		// contain stella-fs. The production sandbox image does; do not pretend
		// this is a Filesystem seam when that capability is absent.
		available, err := session.Exec(ctx, "test -x /opt/stella/bin/stella-fs", sandbox.ExecOptions{})
		if err != nil || available.ExitCode != 0 {
			t.Skip("sandbox image does not provide the mediated filesystem helper")
		}
		withFilesystem, ok := session.(sandbox.FilesystemSession)
		if !ok {
			t.Fatal("docker session does not expose Filesystem")
		}
		filesystem, err := withFilesystem.Filesystem()
		if err != nil {
			t.Fatalf("Filesystem: %v", err)
		}
		defer func() {
			if err := filesystem.Close(); err != nil {
				t.Errorf("Filesystem.Close: %v", err)
			}
		}()

		// Filesystem accepts canonical coordinates by contract. Exec's relative
		// path is resolved from the same nested WorkingDir.
		writeContractFile(t, ctx, filesystem, "/workspace/projects/p/from-filesystem.txt", "from-filesystem")
		got, err := session.Exec(ctx, `cat from-filesystem.txt`, sandbox.ExecOptions{})
		if err != nil || got.ExitCode != 0 || got.Stdout != "from-filesystem" {
			t.Fatalf("exec read filesystem-written project file = %+v, %v", got, err)
		}
		got, err = session.Exec(ctx, `printf from-exec > from-exec.txt`, sandbox.ExecOptions{})
		if err != nil || got.ExitCode != 0 {
			t.Fatalf("exec write project file = %+v, %v", got, err)
		}
		readContractFile(t, ctx, filesystem, "/workspace/projects/p/from-exec.txt", "from-exec")

		writeContractFile(t, ctx, filesystem, "/tmp/from-tool.txt", "from-tool")
		got, err = session.Exec(ctx, `cat "$TMPDIR/from-tool.txt"`, sandbox.ExecOptions{})
		if err != nil || got.ExitCode != 0 || got.Stdout != "from-tool" {
			t.Fatalf("exec read filesystem-written temp file = %+v, %v", got, err)
		}

		got, err = session.Exec(ctx, `printf from-exec > "$TMPDIR/from-exec.txt"`, sandbox.ExecOptions{})
		if err != nil || got.ExitCode != 0 {
			t.Fatalf("exec write temp file = %+v, %v", got, err)
		}
		readContractFile(t, ctx, filesystem, "/tmp/from-exec.txt", "from-exec")

		writeContractFile(t, ctx, filesystem, "/tmp/from-exec.txt", "from-tool-overwrite")
		// Docker Desktop may need a brief propagation window for host overwrites.
		deadline := time.Now().Add(2 * time.Second)
		for {
			got, err = session.Exec(ctx, `cat "$TMPDIR/from-exec.txt"`, sandbox.ExecOptions{})
			if err == nil && got.ExitCode == 0 && got.Stdout == "from-tool-overwrite" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("exec read tool overwrite = %+v, %v", got, err)
			}
			time.Sleep(25 * time.Millisecond)
		}
	})

	t.Run("ReadonlyRootfsLeavesMountsWritable", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		got, err := session.Exec(ctx, `touch /stella-rootfs-write 2>/dev/null && exit 1; printf workspace > rootfs-workspace.txt; printf tmp > "$TMPDIR/rootfs-tmp.txt"`, sandbox.ExecOptions{})
		if err != nil || got.ExitCode != 0 {
			t.Fatalf("exec rootfs/mount writes = %+v, %v", got, err)
		}
		if data, err := os.ReadFile(filepath.Join(project, "rootfs-workspace.txt")); err != nil || string(data) != "workspace" {
			t.Fatalf("workspace write = %q, %v", data, err)
		}
		got, err = session.Exec(ctx, `test "$(cat "$TMPDIR/rootfs-tmp.txt")" = tmp`, sandbox.ExecOptions{})
		if err != nil || got.ExitCode != 0 {
			t.Fatalf("exec read tmp file = %+v, %v", got, err)
		}
	})

	t.Run("Exec", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, sandbox.Policy{
			Filesystem: sandbox.FilesystemPolicy{
				WorkingDir: workspace,
			},
			Network: sandbox.NetworkPolicy{
				Mode:    sandbox.NetworkAllowAll,
				Timeout: 10 * time.Second,
			},
			Timeout: 10 * time.Second,
		})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		execResult, err := session.Exec(ctx, "echo hello", sandbox.ExecOptions{})
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}

		if execResult.ExitCode != 0 {
			t.Errorf("ExitCode = %d, want 0", execResult.ExitCode)
		}

		if !contractContainsSubstring(execResult.Stdout, "hello") {
			t.Errorf("Stdout = %q, should contain %q", execResult.Stdout, "hello")
		}
	})
}

func writeContractFile(t *testing.T, ctx context.Context, filesystem sandbox.Filesystem, path, content string) {
	t.Helper()
	length := int64(len(content))
	if err := filesystem.Write(ctx, path, strings.NewReader(content), sandbox.WriteOptions{Perm: 0o600, ContentLength: &length}); err != nil {
		t.Fatalf("Filesystem.Write(%q): %v", path, err)
	}
}

func readContractFile(t *testing.T, ctx context.Context, filesystem sandbox.Filesystem, path, want string) {
	t.Helper()
	reader, _, err := filesystem.Read(ctx, path, sandbox.ReadOptions{MaxBytes: int64(len(want))})
	if err != nil {
		t.Fatalf("Filesystem.Read(%q): %v", path, err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		t.Fatalf("Filesystem.Read(%q): %v", path, readErr)
	}
	if closeErr != nil {
		t.Fatalf("Filesystem.Read(%q) close: %v", path, closeErr)
	}
	if string(got) != want {
		t.Fatalf("Filesystem.Read(%q) = %q, want %q", path, got, want)
	}
}

func contractContainsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
