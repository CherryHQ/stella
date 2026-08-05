package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/internal/fsops"
	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

// maxHelperStderr bounds how much helper stderr is retained for diagnostics.
// Beyond it the drain keeps reading and discarding so the stdcopy demux — which
// writes stdout and stderr from one loop — never blocks on an undrained stream.
const maxHelperStderr = 4 << 10

// Filesystem uses one Docker exec per operation. It intentionally bypasses the
// legacy host resolver: only container coordinates and the opaque session
// container ID reach Docker.
func (s *dockerSession) Filesystem() (sandboxpkg.Filesystem, error) {
	return &dockerFilesystem{session: s}, nil
}

type dockerFilesystem struct{ session *dockerSession }

var (
	_ sandboxpkg.FilesystemSession = (*dockerSession)(nil)
	_ sandboxpkg.Filesystem        = (*dockerFilesystem)(nil)
)

func (f *dockerFilesystem) Close() error { return nil }
func (f *dockerFilesystem) Read(ctx context.Context, p string, o sandboxpkg.ReadOptions) (io.ReadCloser, sandboxpkg.FileInfo, error) {
	cwd, relative, err := f.mount(p, false)
	if err != nil {
		return nil, sandboxpkg.FileInfo{}, err
	}
	payload, err := fsops.EncodeRequest(fsops.Request{Version: fsops.ProtocolVersion, Operation: "read", Path: relative, MaxBytes: o.MaxBytes})
	if err != nil {
		return nil, sandboxpkg.FileInfo{}, err
	}
	handle, err := f.session.client.StartExec(ctx, dockerclient.ExecOptions{ContainerID: f.session.containerID, Command: []string{"/opt/stella/bin/stella-fs"}, Cwd: cwd})
	if err != nil {
		return nil, sandboxpkg.FileInfo{}, f.transportError(false, err)
	}
	// Drain stderr immediately. stdcopy demux fans stdout and stderr out of one
	// loop into synchronous pipes, so a single undrained stderr frame stalls the
	// stdout side and deadlocks both DecodeResponse and Wait.
	stderr := newStderrDrain(handle.Stderr)
	fail := func(err error) (io.ReadCloser, sandboxpkg.FileInfo, error) {
		_ = handle.Stdin.Close()
		_ = handle.Kill()
		_ = handle.Stdout.Close()
		_ = handle.Stderr.Close()
		stderr.wait()
		return nil, sandboxpkg.FileInfo{}, err
	}
	if _, err := handle.Stdin.Write(payload); err != nil {
		return fail(f.transportError(false, err))
	}
	if err := handle.Stdin.Close(); err != nil {
		return fail(f.transportError(false, err))
	}
	response, err := fsops.DecodeReadResponse(handle.Stdout, o.MaxBytes)
	if err != nil {
		return fail(f.transportError(false, err))
	}
	if err := fsops.ResponseError(response); err != nil {
		return fail(err)
	}
	return &dockerReadCloser{reader: io.LimitReader(handle.Stdout, response.BodyLength), stderr: stderr, handle: handle, remaining: response.BodyLength, limit: response.ErrorCode == fsops.ErrorCodeReadLimit}, response.Info, nil
}

func (f *dockerFilesystem) Stat(ctx context.Context, p string) (sandboxpkg.FileInfo, error) {
	response, err := f.call(ctx, p, fsops.Request{Operation: "stat"}, false)
	return response.Info, err
}

func (f *dockerFilesystem) List(ctx context.Context, p string) ([]sandboxpkg.DirEntry, error) {
	response, err := f.call(ctx, p, fsops.Request{Operation: "list"}, false)
	return response.Entries, err
}

func (f *dockerFilesystem) Mkdir(ctx context.Context, p string, perm fs.FileMode) error {
	_, err := f.call(ctx, p, fsops.Request{Operation: "mkdir", Perm: perm}, true)
	return err
}

func (f *dockerFilesystem) Remove(ctx context.Context, p string, recursive bool) error {
	_, err := f.call(ctx, p, fsops.Request{Operation: "remove", Recursive: recursive}, true)
	return err
}

func (f *dockerFilesystem) Rename(ctx context.Context, oldPath, newPath string) error {
	_, err := f.call(ctx, oldPath, fsops.Request{Operation: "rename", NewPath: newPath}, true)
	return err
}

func (f *dockerFilesystem) Write(ctx context.Context, p string, r io.Reader, o sandboxpkg.WriteOptions) error {
	if o.ContentLength == nil {
		return errors.New("docker filesystem: content length is required")
	}
	_, err := f.callBody(ctx, p, fsops.Request{Version: fsops.ProtocolVersion, Operation: "write", Perm: o.Perm, BodyLength: *o.ContentLength}, r, true)
	return err
}

func (f *dockerFilesystem) Upload(ctx context.Context, p string, r io.Reader, o sandboxpkg.WriteOptions) error {
	if o.ContentLength == nil {
		return errors.New("docker filesystem: content length is required")
	}
	_, err := f.callBody(ctx, p, fsops.Request{Version: fsops.ProtocolVersion, Operation: "upload", Perm: o.Perm, BodyLength: *o.ContentLength}, r, true)
	return err
}

type dockerReadCloser struct {
	reader    io.Reader
	stderr    *stderrDrain
	handle    *dockerclient.ExecHandle
	remaining int64
	limit     bool
	done      bool
	closeOnce sync.Once
	closeErr  error
}

func (r *dockerReadCloser) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	if err == nil {
		return n, nil
	}
	if err != io.EOF {
		// Any non-EOF stream error aborts the exec; never leave the pipes or the
		// hijacked attach connection open.
		r.finish(true)
		return n, err
	}
	if r.remaining != 0 {
		// The helper declared a longer body than it delivered; treat the exec as
		// aborted rather than reporting a silently truncated read.
		r.finish(true)
		return n, errors.New("docker stella-fs read: short body")
	}
	if r.done {
		return n, io.EOF
	}
	r.done = true
	// Prove the helper delivered exactly BodyLength before reaping. A trailing
	// stdout byte means stdcopy is still trying to write the synchronous stdout
	// pipe, so Wait would deadlock; treat extra body as a protocol error and
	// fail closed.
	if perr := r.expectStdoutEOF(); perr != nil {
		r.finish(true)
		return n, perr
	}
	// Body fully consumed and clean: reap the exec exactly once so a helper that
	// failed after streaming (nonzero exit, wait failure) surfaces here rather
	// than as a clean EOF. finish(false) releases the connection without an abort.
	code, waitErr := r.handle.Wait()
	r.finish(false)
	if waitErr != nil {
		return n, fmt.Errorf("docker stella-fs read: %w", waitErr)
	}
	if code != 0 {
		if text := r.stderr.text(); text != "" {
			return n, fmt.Errorf("docker stella-fs read exited %d: %s", code, text)
		}
		return n, fmt.Errorf("docker stella-fs read exited %d", code)
	}
	if r.limit {
		return n, sandboxpkg.ErrReadLimit
	}
	return n, io.EOF
}

// expectStdoutEOF confirms the helper wrote nothing past the declared body.
func (r *dockerReadCloser) expectStdoutEOF() error {
	var probe [1]byte
	for {
		n, err := r.handle.Stdout.Read(probe[:])
		if n > 0 {
			return errors.New("docker stella-fs read: unexpected trailing body")
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("docker stella-fs read: %w", err)
		}
		// n == 0 with no error: an io.Pipe never does this, but retry defensively.
	}
}

func (r *dockerReadCloser) Close() error { r.finish(true); return r.closeErr }

// finish tears the stream down exactly once. abort kills a still-running exec;
// otherwise the connection is released after a clean Wait.
func (r *dockerReadCloser) finish(abort bool) {
	r.closeOnce.Do(func() {
		if abort {
			r.closeErr = r.handle.Kill()
		} else {
			r.closeErr = r.handle.Release()
		}
		_ = r.handle.Stdout.Close()
		_ = r.handle.Stderr.Close()
		r.stderr.wait()
	})
}

// stderrDrain consumes an exec's stderr in the background, retaining a bounded
// prefix for diagnostics while discarding the rest so the demux never blocks.
type stderrDrain struct {
	done chan []byte
	buf  []byte
	once sync.Once
}

func newStderrDrain(r io.Reader) *stderrDrain {
	d := &stderrDrain{done: make(chan []byte, 1)}
	go func() {
		buf, _ := io.ReadAll(io.LimitReader(r, maxHelperStderr))
		_, _ = io.Copy(io.Discard, r)
		d.done <- buf
	}()
	return d
}

// wait blocks until the drain goroutine has finished, guaranteeing no leak.
func (d *stderrDrain) wait() { d.once.Do(func() { d.buf = <-d.done }) }

// text returns the retained stderr prefix; it waits for the drain to finish.
func (d *stderrDrain) text() string {
	d.wait()
	return strings.TrimSpace(string(d.buf))
}

func (f *dockerFilesystem) call(ctx context.Context, sandboxPath string, req fsops.Request, mutation bool) (fsops.Response, error) {
	return f.callBody(ctx, sandboxPath, req, nil, mutation)
}

func (f *dockerFilesystem) callBody(ctx context.Context, sandboxPath string, req fsops.Request, body io.Reader, mutation bool) (fsops.Response, error) {
	cwd, relative, err := f.mount(sandboxPath, mutation)
	if err != nil {
		return fsops.Response{}, err
	}
	req.Path = relative
	if req.Operation == "rename" {
		newCwd, newRelative, mountErr := f.mount(req.NewPath, true)
		if mountErr != nil {
			return fsops.Response{}, mountErr
		}
		if newCwd != cwd {
			return fsops.Response{}, errors.New("docker filesystem: cross-mount rename is not supported")
		}
		req.NewPath = newRelative
	}
	if req.Version == 0 {
		req.Version = fsops.ProtocolVersion
	}
	payload, err := fsops.EncodeRequest(req)
	if err != nil {
		return fsops.Response{}, err
	}
	stdin := io.Reader(bytes.NewReader(payload))
	if body != nil {
		stdin = io.MultiReader(stdin, body)
	}
	result, err := f.session.client.Exec(ctx, dockerclient.ExecOptions{ContainerID: f.session.containerID, Command: []string{"/opt/stella/bin/stella-fs"}, Cwd: cwd, Stdin: stdin})
	if err != nil {
		return fsops.Response{}, f.transportError(mutation, err)
	}
	if result.ExitCode != 0 {
		return fsops.Response{}, f.transportError(mutation, fmt.Errorf("helper exit %d: %s", result.ExitCode, result.Stderr))
	}
	response, err := fsops.DecodeResponse(bytes.NewReader(result.Stdout), fsops.KindForOperation(req.Operation))
	if err != nil {
		return fsops.Response{}, f.transportError(mutation, err)
	}
	if err := fsops.ResponseError(response); err != nil {
		return fsops.Response{}, err
	}
	return response, nil
}

func (f *dockerFilesystem) mount(p string, write bool) (string, string, error) {
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return "", "", fmt.Errorf("docker filesystem: path %q is not canonical", p)
	}
	var best dockerclient.Mount
	found := false
	for _, mount := range f.session.mountTable {
		if mount.ContainerPath != sandboxpkg.PathWorkspace && mount.ContainerPath != sandboxpkg.PathUser && mount.ContainerPath != sandboxpkg.PathTemp {
			continue
		}
		if p == mount.ContainerPath || strings.HasPrefix(p, mount.ContainerPath+"/") {
			if found && len(mount.ContainerPath) == len(best.ContainerPath) {
				return "", "", fmt.Errorf("docker filesystem: ambiguous mount %q", mount.ContainerPath)
			}
			if !found || len(mount.ContainerPath) > len(best.ContainerPath) {
				best = mount
				found = true
			}
		}
	}
	if !found {
		return "", "", fmt.Errorf("docker filesystem: path %q is outside mounted roots", p)
	}
	if write && best.ReadOnly {
		return "", "", fmt.Errorf("docker filesystem: path %q is read-only", p)
	}
	return best.ContainerPath, strings.TrimPrefix(strings.TrimPrefix(p, best.ContainerPath), "/"), nil
}

func (f *dockerFilesystem) transportError(mutation bool, err error) error {
	if mutation {
		return fmt.Errorf("%w: docker stella-fs: %w", sandboxpkg.ErrOutcomeUnknown, err)
	}
	return fmt.Errorf("docker stella-fs: %w", err)
}
