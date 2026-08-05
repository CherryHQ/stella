package dockerclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	mobyclient "github.com/moby/moby/client"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
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
	// Kill aborts a still-running exec: it tears down the attach and the demux
	// pipes, interrupting the process.
	Kill func() error
	// Release closes the attach connection after a clean Wait, without implying
	// an abort. Callers that reach normal completion must call it (or Kill) so
	// the hijacked connection is not leaked.
	Release func() error
}

// Exec runs a blocking exec, collecting stdout/stderr into memory.
func (c *Client) Exec(ctx context.Context, opts ExecOptions) (*ExecResult, error) {
	createOpts := buildExecCreateOptions(opts)

	created, err := c.api.ExecCreate(ctx, opts.ContainerID, createOpts)
	if err != nil {
		return nil, fmt.Errorf("dockerclient: exec create: %w", err)
	}

	attach, err := c.api.ExecAttach(ctx, created.ID, mobyclient.ExecAttachOptions{TTY: opts.Tty})
	if err != nil {
		return nil, fmt.Errorf("dockerclient: exec attach: %w", err)
	}
	defer attach.Close()

	// Closing the hijacked connection on cancellation unblocks a demux (and stdin
	// copy) that would otherwise wait forever on a peer that never writes. stop()
	// releases the watcher on normal completion so it never leaks.
	stop := context.AfterFunc(ctx, func() { attach.Close() })
	defer stop()

	if opts.Stdin != nil {
		go func() {
			_, _ = io.Copy(attach.Conn, opts.Stdin)
			_ = attach.CloseWrite()
		}()
	}

	stdoutBuf := sandboxpkg.NewExecOutputBuffer()
	stderrBuf := sandboxpkg.NewExecOutputBuffer()
	copyErr := demuxStreams(stdoutBuf, stderrBuf, attach, opts.Tty)

	if ctx.Err() != nil {
		return &ExecResult{
			ExitCode: -1,
			Stdout:   stdoutBuf.Bytes(),
			Stderr:   stderrBuf.Bytes(),
		}, fmt.Errorf("dockerclient: exec: %w", ctx.Err())
	}
	if copyErr != nil {
		return nil, fmt.Errorf("dockerclient: exec read: %w", copyErr)
	}

	exitCode, err := waitExecExit(ctx, c.api, created.ID)
	if err != nil {
		return nil, err
	}

	return &ExecResult{
		ExitCode: exitCode,
		Stdout:   stdoutBuf.Bytes(),
		Stderr:   stderrBuf.Bytes(),
	}, nil
}

// StartExec is the streaming variant: returns stdout/stderr pipes and a Wait()
// that blocks until the exec finishes and returns its exit code.
func (c *Client) StartExec(ctx context.Context, opts ExecOptions) (*ExecHandle, error) {
	createOpts := buildExecCreateOptions(opts)
	// StartExec always exposes a stdin handle (either opts.Stdin or handle.Stdin),
	// so the daemon side must accept stdin even when opts.Stdin is nil — otherwise
	// writes on handle.Stdin are silently dropped.
	createOpts.AttachStdin = true

	created, err := c.api.ExecCreate(ctx, opts.ContainerID, createOpts)
	if err != nil {
		return nil, fmt.Errorf("dockerclient: exec create: %w", err)
	}

	attach, err := c.api.ExecAttach(ctx, created.ID, mobyclient.ExecAttachOptions{TTY: opts.Tty})
	if err != nil {
		return nil, fmt.Errorf("dockerclient: exec attach: %w", err)
	}

	handle := &ExecHandle{}

	if opts.Stdin != nil {
		go func() {
			_, _ = io.Copy(attach.Conn, opts.Stdin)
			_ = attach.CloseWrite()
		}()
	} else {
		handle.Stdin = &writeCloserAdapter{
			w:     attach.Conn,
			close: attach.CloseWrite,
		}
	}

	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	handle.Stdout = stdoutR
	handle.Stderr = stderrR

	// On cancellation, tear the transport down so a blocked demux, a blocked
	// DecodeResponse read on Stdout, or a blocked Stdin write all return promptly
	// with the context error. stopCancel() (called by Kill/Release) releases the
	// watcher on normal completion so it never leaks. Every close below is
	// idempotent, so the watcher racing with Kill/Release is safe.
	stopCancel := context.AfterFunc(ctx, func() {
		attach.Close()
		_ = stdoutW.CloseWithError(ctx.Err())
		_ = stderrW.CloseWithError(ctx.Err())
	})

	demuxDone := make(chan error, 1)
	go func() {
		err := demuxStreams(stdoutW, stderrW, attach, opts.Tty)
		_ = stdoutW.Close()
		_ = stderrW.Close()
		demuxDone <- err
	}()

	handle.Wait = func() (int, error) {
		if err := <-demuxDone; err != nil {
			return -1, fmt.Errorf("dockerclient: exec read: %w", err)
		}
		return waitExecExit(ctx, c.api, created.ID)
	}

	handle.Kill = func() error {
		stopCancel()
		attach.Close()
		_ = stdoutW.Close()
		_ = stderrW.Close()
		return nil
	}

	// Release only closes the hijacked connection; the demux goroutine has
	// already closed the pipe writers by the time a caller reaches Wait. Close
	// is idempotent, so a later Kill (or a second Release) is harmless.
	handle.Release = func() error {
		stopCancel()
		attach.Close()
		return nil
	}

	return handle, nil
}

// writeCloserAdapter turns an io.Writer + Close hook into an io.WriteCloser.
type writeCloserAdapter struct {
	w     io.Writer
	close func() error
}

func (a *writeCloserAdapter) Write(p []byte) (int, error) { return a.w.Write(p) }
func (a *writeCloserAdapter) Close() error {
	if a.close == nil {
		return nil
	}
	return a.close()
}

// demuxStreams reads from the hijacked response and splits stdout/stderr.
// When TTY is true, the stream is not multiplexed; everything goes to stdout.
func demuxStreams(stdout, stderr io.Writer, attach mobyclient.ExecAttachResult, tty bool) error {
	if tty {
		if _, err := io.Copy(stdout, attach.Reader); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	}
	if _, err := stdcopy.StdCopy(stdout, stderr, attach.Reader); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// waitExecExit polls ExecInspect until the exec has finished and returns its
// exit code. Polls every 50ms; caller's ctx bounds total wait time.
func waitExecExit(ctx context.Context, api API, execID string) (int, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		res, err := api.ExecInspect(ctx, execID, mobyclient.ExecInspectOptions{})
		if err != nil {
			return -1, fmt.Errorf("dockerclient: exec inspect %s: %w", execID, err)
		}
		if !res.Running {
			return res.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, fmt.Errorf("dockerclient: exec wait: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// buildExecCreateOptions translates ExecOptions into the SDK request.
// Pure function so tests can assert the wiring without a daemon.
func buildExecCreateOptions(opts ExecOptions) mobyclient.ExecCreateOptions {
	return mobyclient.ExecCreateOptions{
		User:         opts.User,
		TTY:          opts.Tty,
		AttachStdin:  opts.Stdin != nil,
		AttachStdout: true,
		AttachStderr: true,
		Env:          envSlice(opts.Env),
		WorkingDir:   opts.Cwd,
		Cmd:          opts.Command,
	}
}
