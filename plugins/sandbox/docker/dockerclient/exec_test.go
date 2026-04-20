package dockerclient

import (
	"strings"
	"testing"
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
