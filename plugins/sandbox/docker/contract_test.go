package docker_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"

	dockerplugin "github.com/CherryHQ/stella/plugins/sandbox/docker"
	dockerclient "github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"

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

func dockerContractRequired() bool {
	return os.Getenv("STELLA_REQUIRE_DOCKER_CONTRACT") == "1"
}

func dockerPreflightForTest(t *testing.T, ctx context.Context) {
	t.Helper()
	cfg := dockerplugin.PreflightConfig{Docker: dockerplugin.Config{Image: dockerContractImage}}
	preflightCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := dockerplugin.Preflight(preflightCtx, cfg); err != nil {
		if dockerContractRequired() {
			t.Fatalf("required docker preflight failed: %v", err)
		}
		t.Skipf("docker preflight failed (image unavailable): %v", err)
	}
}

func TestSessionContract(t *testing.T) {
	t.Run("DockerFactory", func(t *testing.T) {
		ctx := context.Background()
		if !dockerAvailable(ctx) {
			if dockerContractRequired() {
				t.Fatal("required docker daemon is not reachable")
			}
			t.Skip("docker daemon not reachable; skipping DockerFactory contract test")
		}
		dockerPreflightForTest(t, ctx)
		before := dockerSessionContainers(t, ctx)
		stellaHome, err := os.MkdirTemp(".", "docker-contract-stella-home-")
		if err != nil {
			t.Fatal(err)
		}
		stellaHome, err = filepath.Abs(stellaHome)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(stellaHome) })
		factory, err := dockerplugin.NewFactory(dockerplugin.Config{
			Image:       dockerContractImage,
			StellaHome:  stellaHome,
			RuntimeMode: dockerplugin.DockerSandboxModeHost,
		})
		if err != nil {
			t.Fatalf("NewFactory: %v", err)
		}
		testSessionContract(t, factory)
		assertNoNewDockerSessionContainers(t, ctx, before)
	})
}

func testSessionContract(t *testing.T, factory sandbox.Factory) {
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

	policy := sandbox.Policy{
		Filesystem: sandbox.FilesystemPolicy{
			WorkingDir: workspace,
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

		if session == nil {
			t.Fatal("session should be non-nil")
		}

		if !session.Alive() {
			t.Error("session should be alive after creation")
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
			t.Errorf("second Close: %v", err)
		}
	})

	t.Run("HostConsistency", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		if got := session.WorkingDir(); got != policy.Filesystem.WorkingDir {
			t.Errorf("WorkingDir() = %q, want %q", got, policy.Filesystem.WorkingDir)
		}

		resolved, err := session.ResolvePath("test.txt")
		if err != nil {
			t.Errorf("ResolvePath: %v", err)
		}

		expected := filepath.Join(policy.Filesystem.WorkingDir, "test.txt")
		if resolved != expected {
			t.Errorf("ResolvePath(%q) = %q, want %q", "test.txt", resolved, expected)
		}
	})

	t.Run("TempDirSharedByFileToolsAndExec", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		fromTool, err := session.ResolveWritePath("/tmp/from-tool.txt")
		if err != nil {
			t.Fatalf("ResolveWritePath(tool file): %v", err)
		}
		if err := os.WriteFile(fromTool, []byte("from-tool"), 0o600); err != nil {
			t.Fatalf("write tool file: %v", err)
		}
		got, err := session.Exec(ctx, `cat "$TMPDIR/from-tool.txt" && printf from-exec-overwrite > "$TMPDIR/from-tool.txt"`, sandbox.ExecOptions{})
		if err != nil || got.ExitCode != 0 || got.Stdout != "from-tool" {
			t.Fatalf("exec read/write tool file = %+v, %v", got, err)
		}
		data, err := os.ReadFile(fromTool)
		if err != nil || string(data) != "from-exec-overwrite" {
			t.Fatalf("file tool read exec overwrite = %q, %v", data, err)
		}

		got, err = session.Exec(ctx, `printf from-exec > "$TMPDIR/from-exec.txt"`, sandbox.ExecOptions{})
		if err != nil || got.ExitCode != 0 {
			t.Fatalf("exec write temp file = %+v, %v", got, err)
		}
		fromExec, err := session.ResolvePath("/tmp/from-exec.txt")
		if err != nil {
			t.Fatalf("ResolvePath(exec file): %v", err)
		}
		data, err = os.ReadFile(fromExec)
		if err != nil || string(data) != "from-exec" {
			t.Fatalf("file tool read exec file = %q, %v", data, err)
		}
		if fromExec == fromTool {
			t.Fatalf("distinct sandbox paths resolved to the same host path %q", fromExec)
		}
		if err := os.WriteFile(fromExec, []byte("from-tool-overwrite"), 0o600); err != nil {
			t.Fatalf("file tool overwrite exec file: %v", err)
		}
		data, err = os.ReadFile(fromExec)
		if err != nil || string(data) != "from-tool-overwrite" {
			t.Fatalf("host did not observe tool overwrite = %q, %v", data, err)
		}
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

	t.Run("ExecTimeout", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		started := time.Now()
		_, err = session.Exec(ctx, "sleep 5", sandbox.ExecOptions{Timeout: 150 * time.Millisecond})
		if err == nil {
			t.Fatal("Exec succeeded after its deadline")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Exec error = %v, want context deadline exceeded", err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("Exec timeout took %s, want under 2s", elapsed)
		}
	})
}

func dockerSessionContainers(t *testing.T, ctx context.Context) map[string]struct{} {
	t.Helper()
	client, err := dockerclient.New()
	if err != nil {
		t.Fatalf("create docker client for residue check: %v", err)
	}
	defer func() { _ = client.Close() }()

	filters := mobyclient.Filters{}.Add("label", dockerclient.LabelSessionID)
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := client.ContainerList(listCtx, mobyclient.ContainerListOptions{
		All:     true,
		Filters: filters,
	})
	if err != nil {
		t.Fatalf("list docker session containers: %v", err)
	}
	ids := make(map[string]struct{}, len(result.Items))
	for _, container := range result.Items {
		ids[container.ID] = struct{}{}
	}
	return ids
}

func assertNoNewDockerSessionContainers(
	t *testing.T,
	ctx context.Context,
	before map[string]struct{},
) {
	t.Helper()
	after := dockerSessionContainers(t, ctx)
	var leaked []string
	for id := range after {
		if _, existed := before[id]; !existed {
			leaked = append(leaked, id)
		}
	}
	if len(leaked) > 0 {
		t.Fatalf("docker contract left %d Stella sandbox container(s): %v", len(leaked), leaked)
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
