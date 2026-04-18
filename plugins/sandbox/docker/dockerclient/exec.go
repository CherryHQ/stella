package dockerclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
)

// ExecOptions configures a docker exec call.
type ExecOptions struct {
	ContainerID string
	Command     []string // argv — not a shell string
	Cwd         string   // absolute in-container path
	Env         map[string]string
	User        string    // optional override
	Stdin       io.Reader // optional
	Tty         bool      // default false
}

// ExecResult holds the result of a blocking Exec call.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// ExecHandle is the streaming variant returned by StartExec.
type ExecHandle struct {
	// Stdin is non-nil only when opts.Stdin was nil (i.e. we opened a pipe for the caller).
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser
	// Wait blocks until the exec finishes and returns the exit code.
	Wait func() (int, error)
	// Kill terminates the underlying process.
	Kill func() error
}

// Exec runs a blocking `docker exec`. Collects stdout/stderr into memory.
// If ctx is canceled, exec.CommandContext kills the local process.
func (c *Client) Exec(ctx context.Context, opts ExecOptions) (*ExecResult, error) {
	args := buildExecArgs(opts)
	cmd := exec.CommandContext(ctx, c.binaryPath, args...)

	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if ctx.Err() != nil {
			return &ExecResult{ExitCode: -1, Stdout: stdoutBuf.Bytes(), Stderr: stderrBuf.Bytes()},
				fmt.Errorf("docker exec: %w", ctx.Err())
		}
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("docker exec: %w: %s", err, stderrBuf.String())
		}
	}

	return &ExecResult{
		ExitCode: exitCode,
		Stdout:   stdoutBuf.Bytes(),
		Stderr:   stderrBuf.Bytes(),
	}, nil
}

// StartExec is the streaming variant: returns stdout/stderr pipes and a Wait()
// that blocks until the exec finishes and returns exit code.
func (c *Client) StartExec(ctx context.Context, opts ExecOptions) (*ExecHandle, error) {
	args := buildExecArgs(opts)
	cmd := exec.CommandContext(ctx, c.binaryPath, args...)

	handle := &ExecHandle{}

	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
		// handle.Stdin stays nil — caller already owns the reader
	} else {
		wp, err := cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("docker exec: stdin pipe: %w", err)
		}
		handle.Stdin = wp
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("docker exec: stdout pipe: %w", err)
	}
	handle.Stdout = stdoutPipe

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("docker exec: stderr pipe: %w", err)
	}
	handle.Stderr = stderrPipe

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("docker exec: start: %w", err)
	}

	handle.Wait = func() (int, error) {
		err := cmd.Wait()
		if err == nil {
			return 0, nil
		}
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return -1, fmt.Errorf("docker exec: wait: %w", err)
	}

	handle.Kill = func() error {
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}

	return handle, nil
}

// buildExecArgs constructs the argv for `docker exec`.
func buildExecArgs(opts ExecOptions) []string {
	args := []string{"exec"}

	if opts.Stdin != nil {
		args = append(args, "-i")
	}
	if opts.Tty {
		args = append(args, "-t")
	}

	if opts.Cwd != "" {
		args = append(args, "--workdir", opts.Cwd)
	}

	if opts.User != "" {
		args = append(args, "--user", opts.User)
	}

	// Sort env keys for determinism.
	envKeys := make([]string, 0, len(opts.Env))
	for k := range opts.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		args = append(args, "--env", fmt.Sprintf("%s=%s", k, opts.Env[k]))
	}

	args = append(args, opts.ContainerID)
	args = append(args, opts.Command...)

	return args
}
