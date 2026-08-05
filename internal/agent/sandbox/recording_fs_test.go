package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// recordingReadCloser exposes a scripted read stream and records whether it was
// closed, so tests can prove the tools always drain and close the reader.
type recordingReadCloser struct {
	r      io.Reader
	closed bool
}

func (rc *recordingReadCloser) Read(p []byte) (int, error) { return rc.r.Read(p) }
func (rc *recordingReadCloser) Close() error               { rc.closed = true; return nil }

// erroringReader yields content bytes then a terminal error, modeling a stream
// that fails after the initial Read succeeds.
type erroringReader struct {
	data []byte
	err  error
	pos  int
}

func (e *erroringReader) Read(p []byte) (int, error) {
	if e.pos < len(e.data) {
		n := copy(p, e.data[e.pos:])
		e.pos += n
		return n, nil
	}
	return 0, e.err
}

// recordingFS is a fully in-memory provider-neutral Filesystem. It never touches
// the host, so any successful tool result is proof the operation went through
// this boundary rather than an os.* fallback.
type recordingFS struct {
	readStream io.Reader // consumed by the next Read; defaults to readContent
	readInfo   pkgsandbox.FileInfo
	readErr    error
	statInfo   pkgsandbox.FileInfo
	statErr    error
	writeErr   error
	mkdirErr   error

	reads      []recordedRead
	writes     []recordedWrite
	mkdirs     []string
	closeCount int
	lastReader *recordingReadCloser
}

type recordedRead struct {
	path     string
	maxBytes int64
}

type recordedWrite struct {
	path          string
	content       []byte
	contentLength *int64
	perm          fs.FileMode
}

func (f *recordingFS) Close() error { f.closeCount++; return nil }

func (f *recordingFS) Read(_ context.Context, p string, o pkgsandbox.ReadOptions) (io.ReadCloser, pkgsandbox.FileInfo, error) {
	f.reads = append(f.reads, recordedRead{path: p, maxBytes: o.MaxBytes})
	if f.readErr != nil {
		return nil, pkgsandbox.FileInfo{}, f.readErr
	}
	stream := f.readStream
	if stream == nil {
		stream = bytes.NewReader(nil)
	}
	rc := &recordingReadCloser{r: stream}
	f.lastReader = rc
	return rc, f.readInfo, nil
}

func (f *recordingFS) Write(_ context.Context, p string, r io.Reader, o pkgsandbox.WriteOptions) error {
	content, _ := io.ReadAll(r)
	f.writes = append(f.writes, recordedWrite{path: p, content: content, contentLength: o.ContentLength, perm: o.Perm})
	return f.writeErr
}

func (f *recordingFS) Upload(ctx context.Context, p string, r io.Reader, o pkgsandbox.WriteOptions) error {
	return f.Write(ctx, p, r, o)
}

func (f *recordingFS) Stat(_ context.Context, _ string) (pkgsandbox.FileInfo, error) {
	return f.statInfo, f.statErr
}

func (f *recordingFS) List(_ context.Context, _ string) ([]pkgsandbox.DirEntry, error) {
	return nil, nil
}

func (f *recordingFS) Mkdir(_ context.Context, p string, _ fs.FileMode) error {
	f.mkdirs = append(f.mkdirs, p)
	return f.mkdirErr
}
func (f *recordingFS) Remove(_ context.Context, _ string, _ bool) error { return nil }
func (f *recordingFS) Rename(_ context.Context, _, _ string) error      { return nil }

// recordingSession hands the tools one shared recordingFS so a test can inspect
// exactly what the tool asked the boundary to do.
type recordingSession struct {
	pkgsandbox.Session
	fs      *recordingFS
	policy  pkgsandbox.Policy
	workDir string
}

func (s *recordingSession) Policy() pkgsandbox.Policy { return s.policy }
func (s *recordingSession) WorkingDir() string {
	if s.workDir == "" {
		return pkgsandbox.PathWorkspace
	}
	return s.workDir
}
func (s *recordingSession) Filesystem() (pkgsandbox.Filesystem, error) { return s.fs, nil }

func newRecordingSession(f *recordingFS) *recordingSession {
	return &recordingSession{Session: pkgsandbox.NopSession(), fs: f, workDir: pkgsandbox.PathWorkspace}
}

func TestReadToolUsesCanonicalPathBoundedReadAndClosesStream(t *testing.T) {
	f := &recordingFS{readStream: bytes.NewReader([]byte("hello\nworld\n"))}
	session := newRecordingSession(f)
	out, err := newReadTool(session).Execute(context.Background(), map[string]any{"path": "notes/hello.txt"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out == "" {
		t.Fatal("expected file content")
	}
	if len(f.reads) != 1 {
		t.Fatalf("read calls = %d, want 1", len(f.reads))
	}
	if got := f.reads[0].path; got != "/workspace/notes/hello.txt" {
		t.Fatalf("read path = %q, want canonical working-dir join", got)
	}
	if f.reads[0].maxBytes != coreToolReadMaxBytes {
		t.Fatalf("read MaxBytes = %d, want %d", f.reads[0].maxBytes, coreToolReadMaxBytes)
	}
	if f.lastReader == nil || !f.lastReader.closed {
		t.Fatal("read stream was not closed")
	}
	if f.closeCount != 1 {
		t.Fatalf("filesystem closed %d times, want 1", f.closeCount)
	}
}

func TestReadToolMapsStreamReadLimit(t *testing.T) {
	f := &recordingFS{readStream: &erroringReader{err: pkgsandbox.ErrReadLimit}}
	session := newRecordingSession(f)
	_, err := newReadTool(session).Execute(context.Background(), map[string]any{"path": "/workspace/big.bin"})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("read limit")) {
		t.Fatalf("read err = %v, want a read-limit message", err)
	}
	if f.lastReader == nil || !f.lastReader.closed {
		t.Fatal("read stream not closed after limit")
	}
	if f.closeCount != 1 {
		t.Fatalf("filesystem closed %d times, want 1", f.closeCount)
	}
}

func TestReadToolClosesStreamAndFilesystemOnStreamError(t *testing.T) {
	f := &recordingFS{readStream: &erroringReader{data: []byte("partial"), err: errors.New("stream broke")}}
	session := newRecordingSession(f)
	if _, err := newReadTool(session).Execute(context.Background(), map[string]any{"path": "/workspace/x"}); err == nil {
		t.Fatal("expected stream error")
	}
	if f.lastReader == nil || !f.lastReader.closed {
		t.Fatal("read stream not closed after stream error")
	}
	if f.closeCount != 1 {
		t.Fatalf("filesystem closed %d times, want 1", f.closeCount)
	}
}

func TestReadToolMapsAcquisitionReadLimit(t *testing.T) {
	f := &recordingFS{readErr: pkgsandbox.ErrReadLimit}
	session := newRecordingSession(f)
	_, err := newReadTool(session).Execute(context.Background(), map[string]any{"path": "/workspace/big.bin"})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("read limit")) {
		t.Fatalf("read err = %v, want read-limit message from acquisition", err)
	}
	if f.closeCount != 1 {
		t.Fatalf("filesystem closed %d times, want 1", f.closeCount)
	}
}

func TestWriteToolSetsContentLengthPermAndMkdir(t *testing.T) {
	f := &recordingFS{}
	session := newRecordingSession(f)
	if _, err := newWriteTool(session).Execute(context.Background(), map[string]any{"path": "out/file.txt", "content": "payload"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(f.mkdirs) != 1 || f.mkdirs[0] != "/workspace/out" {
		t.Fatalf("mkdir calls = %v, want parent dir created", f.mkdirs)
	}
	if len(f.writes) != 1 {
		t.Fatalf("write calls = %d, want 1", len(f.writes))
	}
	w := f.writes[0]
	if w.path != "/workspace/out/file.txt" {
		t.Fatalf("write path = %q, want canonical", w.path)
	}
	if string(w.content) != "payload" {
		t.Fatalf("write content = %q", w.content)
	}
	if w.contentLength == nil || *w.contentLength != int64(len("payload")) {
		t.Fatalf("write ContentLength = %v, want exact byte length", w.contentLength)
	}
	if w.perm != fs.FileMode(0o644) {
		t.Fatalf("write Perm = %v, want 0644", w.perm)
	}
	if f.closeCount != 1 {
		t.Fatalf("filesystem closed %d times, want 1", f.closeCount)
	}
}

func TestWriteToolDoesNotRetryOnOutcomeUnknown(t *testing.T) {
	f := &recordingFS{writeErr: pkgsandbox.ErrOutcomeUnknown}
	session := newRecordingSession(f)
	_, err := newWriteTool(session).Execute(context.Background(), map[string]any{"path": "/workspace/x.txt", "content": "data"})
	if !errors.Is(err, pkgsandbox.ErrOutcomeUnknown) {
		t.Fatalf("write err = %v, want ErrOutcomeUnknown surfaced", err)
	}
	if len(f.writes) != 1 {
		t.Fatalf("write attempts = %d, want exactly 1 (no retry on unknown outcome)", len(f.writes))
	}
}

func TestEditToolReadsBoundedWritesExactAndCanonical(t *testing.T) {
	f := &recordingFS{readStream: bytes.NewReader([]byte("foo bar"))}
	session := newRecordingSession(f)
	if _, err := newEditTool(session).Execute(context.Background(), map[string]any{"path": "src/edit.txt", "old_string": "foo", "new_string": "baz"}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if len(f.reads) != 1 || f.reads[0].path != "/workspace/src/edit.txt" || f.reads[0].maxBytes != coreToolReadMaxBytes {
		t.Fatalf("edit read = %+v, want one bounded canonical read", f.reads)
	}
	if len(f.writes) != 1 {
		t.Fatalf("edit write calls = %d, want 1", len(f.writes))
	}
	w := f.writes[0]
	if w.path != "/workspace/src/edit.txt" || string(w.content) != "baz bar" {
		t.Fatalf("edit write = %q at %q, want canonical replacement", w.content, w.path)
	}
	if w.contentLength == nil || *w.contentLength != int64(len("baz bar")) || w.perm != fs.FileMode(0o644) {
		t.Fatalf("edit write metadata = len:%v perm:%v", w.contentLength, w.perm)
	}
	if f.lastReader == nil || !f.lastReader.closed || f.closeCount != 1 {
		t.Fatalf("edit did not close stream/filesystem: readerClosed=%v closes=%d", f.lastReader != nil && f.lastReader.closed, f.closeCount)
	}
}

func TestEditToolDoesNotRetryOnOutcomeUnknown(t *testing.T) {
	f := &recordingFS{readStream: bytes.NewReader([]byte("foo bar")), writeErr: pkgsandbox.ErrOutcomeUnknown}
	session := newRecordingSession(f)
	_, err := newEditTool(session).Execute(context.Background(), map[string]any{"path": "/workspace/edit.txt", "old_string": "foo", "new_string": "baz"})
	if !errors.Is(err, pkgsandbox.ErrOutcomeUnknown) {
		t.Fatalf("edit err = %v, want ErrOutcomeUnknown surfaced", err)
	}
	if len(f.writes) != 1 {
		t.Fatalf("edit write attempts = %d, want exactly 1 (no retry)", len(f.writes))
	}
}

// nilFilesystemSession models a misbehaving provider that returns a nil
// Filesystem with a nil error; the tools must reject it, never dereference it.
type nilFilesystemSession struct{ pkgsandbox.Session }

func (nilFilesystemSession) WorkingDir() string { return pkgsandbox.PathWorkspace }
func (nilFilesystemSession) Filesystem() (pkgsandbox.Filesystem, error) {
	return nil, nil
}

func TestToolsRejectNilFilesystem(t *testing.T) {
	session := nilFilesystemSession{Session: pkgsandbox.NopSession()}
	cases := []struct {
		name string
		run  func() (string, error)
	}{
		{"read", func() (string, error) {
			return newReadTool(session).Execute(context.Background(), map[string]any{"path": "/workspace/a"})
		}},
		{"write", func() (string, error) {
			return newWriteTool(session).Execute(context.Background(), map[string]any{"path": "/workspace/a", "content": "x"})
		}},
		{"edit", func() (string, error) {
			return newEditTool(session).Execute(context.Background(), map[string]any{"path": "/workspace/a", "old_string": "x", "new_string": "y"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.run()
			if err == nil || !bytes.Contains([]byte(err.Error()), []byte("nil filesystem")) {
				t.Fatalf("%s err = %v, want a nil-filesystem rejection", tc.name, err)
			}
		})
	}
}

func TestNewToolsRejectsNilFilesystemProbe(t *testing.T) {
	if tools := NewTools(nilFilesystemSession{Session: pkgsandbox.NopSession()}, "", nil); tools != nil {
		t.Fatalf("NewTools returned %d tools for a nil-filesystem provider, want nil", len(tools))
	}
}

func TestToolsExpandLeadingVariableToCanonicalRoot(t *testing.T) {
	f := &recordingFS{readStream: bytes.NewReader([]byte("data"))}
	session := &recordingSession{
		Session: pkgsandbox.NopSession(),
		fs:      f,
		workDir: pkgsandbox.PathWorkspace,
		policy:  pkgsandbox.Policy{Env: map[string]string{pkgsandbox.EnvStellaAssetsDir: pkgsandbox.PathUser + "/assets"}},
	}
	if _, err := newReadTool(session).Execute(context.Background(), map[string]any{"path": "$STELLA_ASSETS_DIR/report.txt"}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(f.reads) != 1 || f.reads[0].path != "/user/assets/report.txt" {
		t.Fatalf("variable expansion path = %+v, want /user/assets/report.txt", f.reads)
	}
}
