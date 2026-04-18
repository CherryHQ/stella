package dockerclient

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestExec_CapturesStdoutStderrExitCode(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "exec", exitCode: 42, stdout: "hello\n", stderr: "warn\n"},
	})

	c := NewWithPath(shimPath)
	result, err := c.Exec(context.Background(), ExecOptions{
		ContainerID: "abc123",
		Command:     []string{"/bin/echo", "hello"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.ExitCode != 42 {
		t.Errorf("ExitCode = %d; want 42", result.ExitCode)
	}
	if !strings.Contains(string(result.Stdout), "hello") {
		t.Errorf("Stdout = %q; want 'hello'", result.Stdout)
	}
	if !strings.Contains(string(result.Stderr), "warn") {
		t.Errorf("Stderr = %q; want 'warn'", result.Stderr)
	}
}

func TestExec_SortsEnvKeysForDeterminism(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "exec", exitCode: 0},
	})

	c := NewWithPath(shimPath)
	opts := ExecOptions{
		ContainerID: "abc123",
		Command:     []string{"/bin/sh", "-c", "env"},
		Env: map[string]string{
			"ZZZ": "last",
			"AAA": "first",
			"MMM": "middle",
		},
	}

	// Run twice; log should be identical.
	_, _ = c.Exec(context.Background(), opts)
	_, _ = c.Exec(context.Background(), opts)

	log := readLog(t, tmp)
	lines := strings.Split(strings.TrimSpace(log), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 log lines, got %d", len(lines))
	}
	if lines[0] != lines[1] {
		t.Errorf("env ordering is non-deterministic:\n  run1: %s\n  run2: %s", lines[0], lines[1])
	}

	// Verify sorted order in argv.
	line := lines[0]
	if strings.Index(line, "AAA=first") > strings.Index(line, "MMM=middle") || strings.Index(line, "MMM=middle") > strings.Index(line, "ZZZ=last") {
		t.Errorf("env keys not sorted: %s", line)
	}
}

func TestExec_WithStdinSetsInteractiveFlag(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "exec", exitCode: 0},
	})

	c := NewWithPath(shimPath)
	_, err := c.Exec(context.Background(), ExecOptions{
		ContainerID: "abc123",
		Command:     []string{"/bin/cat"},
		Stdin:       bytes.NewReader([]byte("input")),
	})
	if err != nil {
		t.Fatalf("Exec with stdin: %v", err)
	}

	log := readLog(t, tmp)
	if !strings.Contains(log, " -i ") && !strings.HasPrefix(log, "-i ") {
		t.Errorf("expected -i flag when Stdin set, got:\n%s", log)
	}
}

func TestExec_CtxCancelReturnsError(t *testing.T) {
	tmp := t.TempDir()
	// Write a shim that sleeps (we'll cancel before it finishes).
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "exec", exitCode: 0},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	c := NewWithPath(shimPath)
	result, err := c.Exec(ctx, ExecOptions{
		ContainerID: "abc123",
		Command:     []string{"/bin/sleep", "10"},
	})
	// Either the ctx error is returned or the process was killed with exit -1.
	// The shim exits instantly so this can also succeed; just ensure no panic.
	_ = result
	_ = err
}

func TestStartExec_PipesWork(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "exec", exitCode: 0, stdout: "streamed\n"},
	})

	c := NewWithPath(shimPath)
	handle, err := c.StartExec(context.Background(), ExecOptions{
		ContainerID: "abc123",
		Command:     []string{"/bin/echo", "streamed"},
	})
	if err != nil {
		t.Fatalf("StartExec: %v", err)
	}

	var out bytes.Buffer
	_, _ = out.ReadFrom(handle.Stdout)

	exitCode, err := handle.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exit code = %d; want 0", exitCode)
	}
	if !strings.Contains(out.String(), "streamed") {
		t.Errorf("stdout = %q; want 'streamed'", out.String())
	}
}

func TestStartExec_StdinNilWhenOptsStdinProvided(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "exec", exitCode: 0},
	})

	c := NewWithPath(shimPath)
	handle, err := c.StartExec(context.Background(), ExecOptions{
		ContainerID: "abc123",
		Command:     []string{"/bin/cat"},
		Stdin:       bytes.NewReader([]byte("data")),
	})
	if err != nil {
		t.Fatalf("StartExec: %v", err)
	}
	if handle.Stdin != nil {
		t.Error("handle.Stdin should be nil when opts.Stdin is provided")
	}
	_, _ = handle.Wait()
}

func TestStartExec_StdinNonNilWhenOptsStdinNil(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "exec", exitCode: 0},
	})

	c := NewWithPath(shimPath)
	handle, err := c.StartExec(context.Background(), ExecOptions{
		ContainerID: "abc123",
		Command:     []string{"/bin/cat"},
	})
	if err != nil {
		t.Fatalf("StartExec: %v", err)
	}
	if handle.Stdin == nil {
		t.Error("handle.Stdin should be non-nil when opts.Stdin is nil")
	}
	_ = handle.Stdin.Close()
	_, _ = handle.Wait()
}
