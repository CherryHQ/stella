package dockerclient

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
)

func TestBuildExecCreateOptions(t *testing.T) {
	t.Run("basic command", func(t *testing.T) {
		opts := ExecOptions{
			ContainerID: "abc123",
			Command:     []string{"ls", "-la"},
			Cwd:         "/workspace",
			User:        "root",
		}
		co := buildExecCreateOptions(opts)
		if co.User != "root" {
			t.Fatalf("unexpected user: %s", co.User)
		}
		if co.WorkingDir != "/workspace" {
			t.Fatalf("unexpected workdir: %s", co.WorkingDir)
		}
		if len(co.Cmd) != 2 || co.Cmd[0] != "ls" {
			t.Fatalf("unexpected cmd: %v", co.Cmd)
		}
		if !co.AttachStdout || !co.AttachStderr {
			t.Fatal("stdout/stderr should always be attached")
		}
		if co.AttachStdin {
			t.Fatal("stdin should not be attached when opts.Stdin is nil")
		}
	})
	t.Run("with stdin", func(t *testing.T) {
		opts := ExecOptions{
			Command: []string{"cat"},
			Stdin:   strings.NewReader("input"),
		}
		co := buildExecCreateOptions(opts)
		if !co.AttachStdin {
			t.Fatal("stdin should be attached when opts.Stdin is set")
		}
	})
	t.Run("tty flag propagated", func(t *testing.T) {
		opts := ExecOptions{Command: []string{"bash"}, Tty: true}
		co := buildExecCreateOptions(opts)
		if !co.TTY {
			t.Fatal("TTY should be true")
		}
	})
	t.Run("env passed through envSlice", func(t *testing.T) {
		opts := ExecOptions{
			Command: []string{"env"},
			Env:     map[string]string{"FOO": "bar"},
		}
		co := buildExecCreateOptions(opts)
		if len(co.Env) != 1 || co.Env[0] != "FOO=bar" {
			t.Fatalf("unexpected env: %v", co.Env)
		}
	})
}

type blockingExecAPI struct {
	API
	attach mobyclient.ExecAttachResult
}

func (a blockingExecAPI) ExecCreate(
	context.Context,
	string,
	mobyclient.ExecCreateOptions,
) (mobyclient.ExecCreateResult, error) {
	return mobyclient.ExecCreateResult{ID: "exec-contract"}, nil
}

func (a blockingExecAPI) ExecAttach(
	context.Context,
	string,
	mobyclient.ExecAttachOptions,
) (mobyclient.ExecAttachResult, error) {
	return a.attach, nil
}

// TestExecClosesAttachStreamOnContextCancellation pins the behavior required by
// the real Docker contract: a timed-out command must return promptly even when
// the daemon-side attach stream remains open.
func TestExecClosesAttachStreamOnContextCancellation(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	t.Cleanup(func() { _ = daemonConn.Close() })
	attach := mobyclient.NewHijackedResponse(clientConn, "")
	attach.Reader = bufio.NewReader(clientConn)
	client := NewWithAPI(blockingExecAPI{
		attach: mobyclient.ExecAttachResult{HijackedResponse: attach},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := client.Exec(ctx, ExecOptions{
		ContainerID: "container-contract",
		Command:     []string{"sleep", "5"},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Exec error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Exec cancellation took %s, want under 1s", elapsed)
	}
	if result == nil || result.ExitCode != -1 {
		t.Fatalf("Exec result = %#v, want exit code -1", result)
	}
}
