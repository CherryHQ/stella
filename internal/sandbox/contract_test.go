package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	boxshplugin "github.com/vaayne/anna/plugins/sandbox/boxsh"
	dockerplugin "github.com/vaayne/anna/plugins/sandbox/docker"
	dockerclient "github.com/vaayne/anna/plugins/sandbox/docker/dockerclient"

	localplugin "github.com/vaayne/anna/plugins/sandbox/local"
)

// dockerAvailable probes whether the docker daemon is reachable.
// It constructs a client (requires binary on PATH) and calls Version with a short timeout.
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

// dockerPreflightForTest pulls the default sandbox image when it is not already
// present locally. CI hosts have a docker daemon but no pre-pulled image, so
// the contract tests (which call CreateSession directly, bypassing the public
// Preflight entry point) would otherwise fail with "No such image".
//
// A pull failure (no network, rate limit, …) is reported via t.Skip rather
// than t.Fatal: the test's concern is contract conformance, not registry
// availability. Daemon-reachability failures are already handled upstream.
func dockerPreflightForTest(t *testing.T, ctx context.Context) {
	t.Helper()
	cfg := dockerplugin.PreflightConfig{Docker: dockerplugin.Config{AllowPull: true}}
	preflightCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := dockerplugin.Preflight(preflightCtx, cfg); err != nil {
		t.Skipf("docker preflight failed (image unavailable): %v", err)
	}
}

// Contract tests for Session and Host interfaces.
// These tests verify that both local and boxsh backends satisfy the shared contract.

// TestSessionContract runs the full session contract against a factory.
func TestSessionContract(t *testing.T) {
	// Test with local factory (always available)
	t.Run("LocalFactory", func(t *testing.T) {
		testSessionContract(t, localplugin.NewFactory())
	})

	// Test with docker factory if daemon is reachable
	t.Run("DockerFactory", func(t *testing.T) {
		ctx := context.Background()
		if !dockerAvailable(ctx) {
			t.Skip("docker daemon not reachable; skipping DockerFactory contract test")
		}
		dockerPreflightForTest(t, ctx)
		testSessionContract(t, dockerplugin.NewFactory(dockerplugin.Config{AllowPull: true}))
	})

	// Test with boxsh factory if available
	if PlatformRequiresBoxsh() {
		t.Run("BoxshFactory", func(t *testing.T) {
			// Skip if boxsh binary not available
			annaHome := os.Getenv("ANNA_HOME")
			if annaHome == "" {
				t.Skip("ANNA_HOME not set, boxsh binary not available")
			}
			if _, err := boxshplugin.ResolveManagedBoxshPath(annaHome); err != nil {
				t.Skipf("boxsh binary not available: %v", err)
			}

			testSessionContract(t, boxshplugin.NewFactory())
		})
	}
}

func testSessionContract(t *testing.T, factory Factory) {
	ctx := context.Background()
	tempDir := t.TempDir()

	policy := Policy{
		Backend: factory.Name(),
		Relaxed: true, // Use relaxed mode for contract tests
		Filesystem: FilesystemPolicy{
			WorkingDir:   tempDir,
			AllowEscapes: false,
		},
		Network: NetworkPolicy{
			Mode: NetworkDisabled,
		},
		Process: ProcessPolicy{
			Timeout: 10 * time.Second,
		},
	}

	t.Run("CreateAndClose", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		// Session should be alive initially
		if !session.Alive() {
			t.Error("session should be alive after creation")
		}

		// Policy should match
		if got := session.Policy(); got.Backend != policy.Backend {
			t.Errorf("Policy().Backend = %q, want %q", got.Backend, policy.Backend)
		}

		// Host should be non-nil
		if session.Host() == nil {
			t.Error("Host() should return non-nil")
		}

		// Close should succeed
		if err := session.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}

		// After close, session should not be alive
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

		// First close
		if err := session.Close(); err != nil {
			t.Errorf("first Close: %v", err)
		}

		// Second close should be safe (may return error or nil)
		if err := session.Close(); err != nil {
			// Error on second close is acceptable
			t.Logf("second Close returned error (acceptable): %v", err)
		}
	})

	t.Run("HostConsistency", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		host := session.Host()

		// WorkingDir should match policy
		if got := host.WorkingDir(); got != policy.Filesystem.WorkingDir {
			t.Errorf("WorkingDir() = %q, want %q", got, policy.Filesystem.WorkingDir)
		}

		// ResolvePath should work
		resolved, err := host.ResolvePath("test.txt")
		if err != nil {
			t.Errorf("ResolvePath: %v", err)
		}

		expected := filepath.Join(policy.Filesystem.WorkingDir, "test.txt")
		if resolved != expected {
			t.Errorf("ResolvePath(%q) = %q, want %q", "test.txt", resolved, expected)
		}
	})
}

// TestHostContract runs the full host contract tests.
func TestHostContract(t *testing.T) {
	// Test with local factory (always available)
	t.Run("LocalFactory", func(t *testing.T) {
		testHostContract(t, localplugin.NewFactory())
	})

	// Test with docker factory if daemon is reachable
	t.Run("DockerFactory", func(t *testing.T) {
		ctx := context.Background()
		if !dockerAvailable(ctx) {
			t.Skip("docker daemon not reachable; skipping DockerFactory host contract test")
		}
		dockerPreflightForTest(t, ctx)
		testHostContract(t, dockerplugin.NewFactory(dockerplugin.Config{AllowPull: true}))
	})
}

func testHostContract(t *testing.T, factory Factory) {
	ctx := context.Background()
	tempDir := t.TempDir()

	policy := Policy{
		Backend: factory.Name(),
		Relaxed: true,
		Filesystem: FilesystemPolicy{
			WorkingDir:   tempDir,
			AllowEscapes: true,
		},
		Network: NetworkPolicy{
			Mode:    NetworkAllowAll,
			Timeout: 10 * time.Second,
		},
		Process: ProcessPolicy{
			Timeout: 10 * time.Second,
		},
	}

	t.Run("FileOperations", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		host := session.Host()

		// WriteFile
		content := []byte("hello, world")
		writeResult, err := host.WriteFile(ctx, "test.txt", content)
		if err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if writeResult.BytesWritten != len(content) {
			t.Errorf("BytesWritten = %d, want %d", writeResult.BytesWritten, len(content))
		}

		// ReadFile
		readResult, err := host.ReadFile(ctx, "test.txt", 0, 0)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(readResult.Content) != string(content) {
			t.Errorf("Content = %q, want %q", readResult.Content, content)
		}

		// ReadFile with offset and limit
		readResult, err = host.ReadFile(ctx, "test.txt", 7, 5)
		if err != nil {
			t.Fatalf("ReadFile with offset/limit: %v", err)
		}
		if string(readResult.Content) != "world" {
			t.Errorf("Content with offset 7 = %q, want %q", readResult.Content, "world")
		}
		if readResult.NextOffset != 12 {
			t.Errorf("NextOffset = %d, want 12", readResult.NextOffset)
		}

		// Stat
		stat, err := host.Stat(ctx, "test.txt")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if !stat.Exists {
			t.Error("Stat says file does not exist")
		}
		if stat.IsDir {
			t.Error("Stat says file is a directory")
		}
		if stat.Size != int64(len(content)) {
			t.Errorf("Size = %d, want %d", stat.Size, len(content))
		}

		// Non-existent file
		stat, err = host.Stat(ctx, "nonexistent.txt")
		if err != nil {
			t.Fatalf("Stat nonexistent: %v", err)
		}
		if stat.Exists {
			t.Error("Stat says nonexistent file exists")
		}
	})

	t.Run("DirectoryOperations", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		host := session.Host()

		// MkdirAll
		if err := host.MkdirAll(ctx, "subdir/nested", 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		// Verify directory exists
		stat, err := host.Stat(ctx, "subdir/nested")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if !stat.Exists {
			t.Error("directory does not exist after MkdirAll")
		}
		if !stat.IsDir {
			t.Error("created path is not a directory")
		}

		// Write file in nested directory
		_, err = host.WriteFile(ctx, "subdir/nested/file.txt", []byte("nested content"))
		if err != nil {
			t.Fatalf("WriteFile in nested dir: %v", err)
		}

		// ListDir
		entries, err := host.ListDir(ctx, "subdir")
		if err != nil {
			t.Fatalf("ListDir: %v", err)
		}
		if len(entries) != 1 {
			t.Errorf("ListDir returned %d entries, want 1", len(entries))
		}
		if len(entries) > 0 && entries[0].Name != "nested" {
			t.Errorf("entry name = %q, want %q", entries[0].Name, "nested")
		}

		// Remove
		if err := host.Remove(ctx, "subdir/nested/file.txt", false); err != nil {
			t.Errorf("Remove file: %v", err)
		}

		// Remove recursive
		if err := host.Remove(ctx, "subdir", true); err != nil {
			t.Errorf("Remove recursive: %v", err)
		}

		stat, err = host.Stat(ctx, "subdir")
		if err != nil {
			t.Fatalf("Stat after remove: %v", err)
		}
		if stat.Exists {
			t.Error("directory still exists after recursive remove")
		}
	})

	t.Run("EditFile", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		host := session.Host()

		// Create initial file
		initial := "hello, world\nfoo bar\n"
		_, err = host.WriteFile(ctx, "edit_test.txt", []byte(initial))
		if err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		// Edit file
		edits := []Edit{
			{OldText: "world", NewText: "universe"},
			{OldText: "foo", NewText: "baz"},
		}
		editResult, err := host.EditFile(ctx, "edit_test.txt", edits)
		if err != nil {
			t.Fatalf("EditFile: %v", err)
		}
		if editResult.AppliedEdits != 2 {
			t.Errorf("AppliedEdits = %d, want 2", editResult.AppliedEdits)
		}

		// Verify content
		readResult, err := host.ReadFile(ctx, "edit_test.txt", 0, 0)
		if err != nil {
			t.Fatalf("ReadFile after edit: %v", err)
		}

		expected := "hello, universe\nbaz bar\n"
		if string(readResult.Content) != expected {
			t.Errorf("content after edit = %q, want %q", readResult.Content, expected)
		}
	})

	t.Run("Rename", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		host := session.Host()

		_, err = host.WriteFile(ctx, "oldname.txt", []byte("content"))
		if err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		if err := host.Rename(ctx, "oldname.txt", "newname.txt"); err != nil {
			t.Fatalf("Rename: %v", err)
		}

		// Verify old name doesn't exist
		stat, _ := host.Stat(ctx, "oldname.txt")
		if stat.Exists {
			t.Error("oldname.txt still exists after rename")
		}

		// Verify new name exists
		stat, err = host.Stat(ctx, "newname.txt")
		if err != nil {
			t.Fatalf("Stat newname: %v", err)
		}
		if !stat.Exists {
			t.Error("newname.txt does not exist after rename")
		}
	})

	t.Run("CreateTemp", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		host := session.Host()

		tempFile, err := host.CreateTemp(ctx, "", "test-*.txt")
		if err != nil {
			t.Fatalf("CreateTemp: %v", err)
		}

		// Temp file should exist
		stat, err := host.Stat(ctx, tempFile.Path())
		if err != nil {
			t.Fatalf("Stat temp file: %v", err)
		}
		if !stat.Exists {
			t.Error("temp file does not exist")
		}

		// Write to temp file
		_, err = tempFile.Write([]byte("temp content"))
		if err != nil {
			t.Errorf("Write temp file: %v", err)
		}

		// Close temp file
		if err := tempFile.Close(); err != nil {
			t.Errorf("Close temp file: %v", err)
		}
	})

	t.Run("Exec", func(t *testing.T) {
		// Skip on Windows
		if runtime.GOOS == "windows" {
			t.Skip("Exec test skipped on Windows")
		}

		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		host := session.Host()

		// Simple echo command
		execResult, err := host.Exec(ctx, "echo hello", ExecOptions{})
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}

		if execResult.ExitCode != 0 {
			t.Errorf("ExitCode = %d, want 0", execResult.ExitCode)
		}

		if !contains(execResult.Stdout, "hello") {
			t.Errorf("Stdout = %q, should contain %q", execResult.Stdout, "hello")
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
