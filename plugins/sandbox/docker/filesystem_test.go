package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	iofs "io/fs"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"

	"github.com/CherryHQ/stella/internal/fsops"
	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

func TestDockerFilesystemInterruptedMutationIsOutcomeUnknown(t *testing.T) {
	fs := &dockerFilesystem{}
	err := fs.transportError(true, errors.New("exec attach disconnected"))
	if !sandboxpkg.IsOutcomeUnknown(err) {
		t.Fatalf("transportError() = %v, want outcome unknown", err)
	}
	if sandboxpkg.IsOutcomeUnknown(fs.transportError(false, errors.New("read disconnected"))) {
		t.Fatal("read interruption must not claim a write outcome")
	}
}

// --- Mutation path: one ExecCreate, ErrOutcomeUnknown on any interrupted or
// undecodable response. The fake stream is stdcopy-framed so the failure is
// exercised by response decoding, not an accidental demux error. ---

func TestDockerFilesystemWriteAttachFailureIsUnknownOnce(t *testing.T) {
	api := &filesystemExecAPI{attachErr: errors.New("attach lost")}
	if err := writeOne(t, api); !sandboxpkg.IsOutcomeUnknown(err) || api.creates != 1 {
		t.Fatalf("err=%v creates=%d", err, api.creates)
	}
}

func TestDockerFilesystemWriteInterruptedStreamIsUnknownOnce(t *testing.T) {
	// A truncated stdcopy frame (header promises 8 bytes, stream ends early)
	// models an attach interruption mid-response.
	truncated := []byte{1, 0, 0, 0, 0, 0, 0, 8, 'x', 'y'}
	api := &filesystemExecAPI{response: truncated}
	if err := writeOne(t, api); !sandboxpkg.IsOutcomeUnknown(err) || api.creates != 1 {
		t.Fatalf("err=%v creates=%d", err, api.creates)
	}
}

func TestDockerFilesystemWriteMalformedResponseIsUnknownOnce(t *testing.T) {
	api := &filesystemExecAPI{response: frameStdout(fsFrame([]byte("{]")))}
	if err := writeOne(t, api); !sandboxpkg.IsOutcomeUnknown(err) || api.creates != 1 {
		t.Fatalf("err=%v creates=%d", err, api.creates)
	}
}

func TestDockerFilesystemWriteVersionMismatchIsUnknownOnce(t *testing.T) {
	api := &filesystemExecAPI{response: frameStdout(fsFrame([]byte(`{"version":2,"kind":"mutation"}`)))}
	if err := writeOne(t, api); !sandboxpkg.IsOutcomeUnknown(err) || api.creates != 1 {
		t.Fatalf("err=%v creates=%d", err, api.creates)
	}
}

func TestDockerFilesystemWriteKindMismatchIsUnknownOnce(t *testing.T) {
	// A helper that answers a mutation with a read-shaped reply must fail closed
	// as outcome-unknown, not be mistaken for success.
	api := &filesystemExecAPI{response: frameStdout(fsFrame([]byte(`{"version":1,"kind":"read","body_length":0}`)))}
	if err := writeOne(t, api); !sandboxpkg.IsOutcomeUnknown(err) || api.creates != 1 {
		t.Fatalf("err=%v creates=%d", err, api.creates)
	}
}

func TestDockerFilesystemWriteSucceedsOnFramedResponse(t *testing.T) {
	api := &filesystemExecAPI{response: frameStdout(fsFrame([]byte(`{"version":1,"kind":"mutation"}`)))}
	if err := writeOne(t, api); err != nil || api.creates != 1 {
		t.Fatalf("err=%v creates=%d", err, api.creates)
	}
}

func TestDockerFilesystemRenameMapsExistingDestination(t *testing.T) {
	api := &filesystemExecAPI{response: frameStdout(fsFrame([]byte(`{"version":1,"kind":"mutation","error_code":"exist","error":"destination exists"}`)))}
	err := testDockerFilesystem(api).Rename(context.Background(), "/workspace/source", "/workspace/destination")
	if !errors.Is(err, iofs.ErrExist) || sandboxpkg.IsOutcomeUnknown(err) {
		t.Fatalf("rename error = %v, want typed definite iofs.ErrExist", err)
	}
}

func writeOne(t *testing.T, api dockerclient.API) error {
	t.Helper()
	fs := testDockerFilesystem(api)
	length := int64(1)
	return fs.Write(context.Background(), "/workspace/file", strings.NewReader("x"), sandboxpkg.WriteOptions{ContentLength: &length})
}

type filesystemExecAPI struct {
	noopAPI
	creates     int
	attachErr   error
	response    []byte
	request     chan []byte
	fullRequest chan []byte
	wait        <-chan struct{}
	inspects    int
}

func (a *filesystemExecAPI) ExecCreate(_ context.Context, _ string, _ mobyclient.ExecCreateOptions) (mobyclient.ExecCreateResult, error) {
	a.creates++
	return mobyclient.ExecCreateResult{ID: "exec"}, nil
}

func (a *filesystemExecAPI) ExecAttach(_ context.Context, _ string, _ mobyclient.ExecAttachOptions) (mobyclient.ExecAttachResult, error) {
	if a.attachErr != nil {
		return mobyclient.ExecAttachResult{}, a.attachErr
	}
	c, s := net.Pipe()
	go func() {
		var header [4]byte
		_, _ = io.ReadFull(s, header[:])
		data := make([]byte, 4+binary.BigEndian.Uint32(header[:]))
		copy(data, header[:])
		_, _ = io.ReadFull(s, data[4:])
		if a.request != nil {
			a.request <- data
		}
		var req fsops.Request
		_ = json.Unmarshal(data[4:], &req)
		body := make([]byte, req.BodyLength)
		if req.BodyLength > 0 {
			_, _ = io.ReadFull(s, body)
		}
		if a.fullRequest != nil {
			a.fullRequest <- append(data, body...)
		}
		if a.wait != nil {
			<-a.wait
		}
		_, _ = s.Write(a.response)
		_ = s.Close()
	}()
	return mobyclient.ExecAttachResult{HijackedResponse: mobyclient.NewHijackedResponse(c, "application/vnd.docker.raw-stream")}, nil
}

func TestDockerFilesystemPublishManagedSkillUsesSortedManifestAndFullBody(t *testing.T) {
	api := &filesystemExecAPI{response: frameStdout(fsFrame([]byte(`{"version":1,"kind":"mutation"}`))), fullRequest: make(chan []byte, 1)}
	fsys := testDockerFilesystem(api)
	files := []sandboxpkg.ManagedSkillTreeEntry{
		{Path: "SKILL.md", Mode: 0o644, Length: 1, Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("s")), nil }},
		{Path: ".stella-skill.json", Mode: 0o644, Length: 1, Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("m")), nil }},
	}
	if err := fsys.PublishManagedSkill(context.Background(), "/workspace/nested/catalog", "skill", strings.Repeat("a", 64), sandboxpkg.ManagedSkillPublication{Files: files}); err != nil {
		t.Fatal(err)
	}
	wire := <-api.fullRequest
	n := int(binary.BigEndian.Uint32(wire[:4]))
	var req fsops.Request
	if err := json.Unmarshal(wire[4:4+n], &req); err != nil {
		t.Fatal(err)
	}
	if req.CatalogRoot != "nested/catalog" || len(req.Files) != 2 || req.Files[0].Path != ".stella-skill.json" || string(wire[4+n:]) != "ms" {
		t.Fatalf("publication wire = %+v body=%q", req, wire[4+n:])
	}
}

func TestDockerFilesystemPublishRejectsNilOpenBeforeExec(t *testing.T) {
	api := &filesystemExecAPI{}
	files := []sandboxpkg.ManagedSkillTreeEntry{
		{Path: ".stella-skill.json", Mode: 0o644, Length: 2, Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("{}")), nil }},
		{Path: "SKILL.md", Mode: 0o644, Length: 1, Open: nil},
	}
	if err := testDockerFilesystem(api).PublishManagedSkill(context.Background(), "/workspace", "skill", strings.Repeat("a", 64), sandboxpkg.ManagedSkillPublication{Files: files}); err == nil || api.creates != 0 {
		t.Fatalf("err=%v ExecCreate=%d; nil Open must fail before exec", err, api.creates)
	}
}

func TestDockerFilesystemPublishShortAndLongReadersCloseOnceAndFailUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		length     int64
	}{{"short", "x", 2}, {"long", "xyz", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			ready := make(chan struct{})
			api := &filesystemExecAPI{wait: ready}
			var opens, closes int32
			files := []sandboxpkg.ManagedSkillTreeEntry{
				{Path: ".stella-skill.json", Mode: 0o644, Length: 2, Open: func() (io.ReadCloser, error) {
					atomic.AddInt32(&opens, 1)
					return &countedReadCloser{Reader: strings.NewReader("{}"), closes: &closes}, nil
				}},
				{Path: "SKILL.md", Mode: 0o644, Length: tc.length, Open: func() (io.ReadCloser, error) {
					atomic.AddInt32(&opens, 1)
					close(ready)
					return &countedReadCloser{Reader: strings.NewReader(tc.body), closes: &closes}, nil
				}},
			}
			err := testDockerFilesystem(api).PublishManagedSkill(context.Background(), "/workspace", "skill", strings.Repeat("a", 64), sandboxpkg.ManagedSkillPublication{Files: files})
			if !sandboxpkg.IsOutcomeUnknown(err) || api.creates != 1 || atomic.LoadInt32(&opens) != 2 || atomic.LoadInt32(&closes) != 2 {
				t.Fatalf("err=%v creates=%d opens=%d closes=%d", err, api.creates, opens, closes)
			}
		})
	}
}

func TestDockerFilesystemPublishTransportErrorClosesCurrentStreamWithoutRetry(t *testing.T) {
	ready := make(chan struct{})
	api := &filesystemExecAPI{response: []byte{1, 0, 0, 0, 0, 0, 0, 4, 'x'}, wait: ready}
	var opens, closes int32
	files := []sandboxpkg.ManagedSkillTreeEntry{
		{Path: ".stella-skill.json", Mode: 0o644, Length: 2, Open: func() (io.ReadCloser, error) {
			atomic.AddInt32(&opens, 1)
			return &countedReadCloser{Reader: strings.NewReader("{}"), closes: &closes}, nil
		}},
		{Path: "SKILL.md", Mode: 0o644, Length: 1, Open: func() (io.ReadCloser, error) {
			atomic.AddInt32(&opens, 1)
			close(ready)
			return &countedReadCloser{Reader: strings.NewReader("x"), closes: &closes}, nil
		}},
	}
	err := testDockerFilesystem(api).PublishManagedSkill(context.Background(), "/workspace", "skill", strings.Repeat("a", 64), sandboxpkg.ManagedSkillPublication{Files: files})
	if !sandboxpkg.IsOutcomeUnknown(err) || api.creates != 1 || atomic.LoadInt32(&opens) != 2 || atomic.LoadInt32(&closes) != 2 {
		t.Fatalf("err=%v creates=%d opens=%d closes=%d", err, api.creates, opens, closes)
	}
}

func TestDockerFilesystemPublishCancellationClosesCurrentStreamWithoutRetry(t *testing.T) {
	ready := make(chan struct{})
	api := &filesystemExecAPI{wait: ready}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var opens, closes int32
	files := []sandboxpkg.ManagedSkillTreeEntry{
		{Path: ".stella-skill.json", Mode: 0o644, Length: 2, Open: func() (io.ReadCloser, error) {
			atomic.AddInt32(&opens, 1)
			return &countedReadCloser{Reader: strings.NewReader("{}"), closes: &closes}, nil
		}},
		{Path: "SKILL.md", Mode: 0o644, Length: 1, Open: func() (io.ReadCloser, error) {
			atomic.AddInt32(&opens, 1)
			cancel()
			close(ready)
			return &countedReadCloser{Reader: strings.NewReader("x"), closes: &closes}, nil
		}},
	}
	err := testDockerFilesystem(api).PublishManagedSkill(ctx, "/workspace", "skill", strings.Repeat("a", 64), sandboxpkg.ManagedSkillPublication{Files: files})
	if !sandboxpkg.IsOutcomeUnknown(err) || api.creates != 1 || atomic.LoadInt32(&opens) != 2 || atomic.LoadInt32(&closes) != 2 {
		t.Fatalf("err=%v creates=%d opens=%d closes=%d", err, api.creates, opens, closes)
	}
}

func TestManagedPublicationBodyNoProgressAndIdempotentClose(t *testing.T) {
	var closes int32
	b := &managedPublicationBody{files: []sandboxpkg.ManagedSkillTreeEntry{{Path: "x", Length: 1, Open: func() (io.ReadCloser, error) { return &countedReadCloser{Reader: zeroReader{}, closes: &closes}, nil }}}}
	buf := make([]byte, 1)
	_, err := b.Read(buf)
	if !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("err=%v, want io.ErrNoProgress", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&closes); got != 1 {
		t.Fatalf("closes=%d, want 1", got)
	}
}

type countedReadCloser struct {
	io.Reader
	closes *int32
}

func (r *countedReadCloser) Close() error { atomic.AddInt32(r.closes, 1); return nil }

type zeroReader struct{}

func (zeroReader) Read([]byte) (int, error) { return 0, nil }

func TestDockerFilesystemInspectManagedSkillTargetKeepsNestedRelativePath(t *testing.T) {
	payload, err := json.Marshal(fsops.Response{Version: fsops.ProtocolVersion, Kind: fsops.KindManagedSkillTarget})
	if err != nil {
		t.Fatal(err)
	}
	api := &filesystemExecAPI{response: frameStdout(fsFrame(payload)), request: make(chan []byte, 1)}
	fsys := testDockerFilesystem(api)
	if _, err := fsys.InspectManagedSkillTarget(context.Background(), "/workspace/skills/foo"); err != nil {
		t.Fatal(err)
	}
	request := <-api.request
	if len(request) < 4 {
		t.Fatal("missing helper request")
	}
	var got fsops.Request
	if err := json.Unmarshal(request[4:], &got); err != nil {
		t.Fatal(err)
	}
	if got.Operation != "managed_skill_target" || got.Path != "skills/foo" {
		t.Fatalf("request = %+v", got)
	}
}

func TestDockerFilesystemUnpublishManagedSkillUsesOneReapedExecAndTypedConflict(t *testing.T) {
	conflict := fsops.Response{Version: fsops.ProtocolVersion, Kind: fsops.KindMutation, ErrorCode: fsops.ErrorCodeManagedSkillConflict, Error: "selection changed"}
	payload, err := json.Marshal(conflict)
	if err != nil {
		t.Fatal(err)
	}
	api := &filesystemExecAPI{response: frameStdout(fsFrame(payload)), request: make(chan []byte, 1)}
	err = testDockerFilesystem(api).UnpublishManagedSkill(context.Background(), "/workspace/nested", "skill", strings.Repeat("a", 64))
	if !errors.Is(err, sandboxpkg.ErrManagedSkillConflict) || sandboxpkg.IsOutcomeUnknown(err) {
		t.Fatalf("unpublish conflict = %v", err)
	}
	var request fsops.Request
	if err := json.Unmarshal((<-api.request)[4:], &request); err != nil {
		t.Fatal(err)
	}
	if request.Operation != "unpublish_managed_skill" || request.CatalogRoot != "nested" || request.Path != "skill" || request.Digest != strings.Repeat("a", 64) {
		t.Fatalf("request = %+v", request)
	}
	if api.inspects == 0 {
		t.Fatal("unpublish returned without reaping its synchronous exec")
	}
}

func TestDockerFilesystemUnpublishDisconnectIsOutcomeUnknown(t *testing.T) {
	api := &filesystemExecAPI{response: []byte{1, 0, 0, 0, 0, 0, 0, 4, 'x'}}
	err := testDockerFilesystem(api).UnpublishManagedSkill(context.Background(), "/workspace", "skill", strings.Repeat("a", 64))
	if !sandboxpkg.IsOutcomeUnknown(err) || api.creates != 1 {
		t.Fatalf("unpublish disconnect = %v, creates=%d", err, api.creates)
	}
}

func (a *filesystemExecAPI) ExecInspect(context.Context, string, mobyclient.ExecInspectOptions) (mobyclient.ExecInspectResult, error) {
	a.inspects++
	return mobyclient.ExecInspectResult{Running: false, ExitCode: 0}, nil
}

// --- Streaming read path: exercise the drain/wait/kill lifecycle end to end. ---

func TestDockerFilesystemReadExactBody(t *testing.T) {
	api := newStreamAPI(readResponse(5, ""), []byte("hello"), nil, 0)
	data, err := readAll(t, api)
	if err != nil || data != "hello" {
		t.Fatalf("read = %q, %v", data, err)
	}
	// Clean release is distinguished from an early abort by two signals: the exec
	// was waited on (inspects == 1) and the attach connection was released
	// (closes == 1, not leaked).
	if got := atomic.LoadInt32(&api.inspects); got != 1 {
		t.Fatalf("wait ran %d times, want exactly one", got)
	}
	if got := atomic.LoadInt32(&api.closes); got != 1 {
		t.Fatalf("clean completion released the attach %d times, want exactly one", got)
	}
}

func TestDockerFilesystemReadExtraBodyIsProtocolError(t *testing.T) {
	// The helper declares 3 bytes but streams 5. The trailing bytes must be
	// caught before Wait (which would otherwise deadlock on the stdout pipe) and
	// the exec aborted.
	api := newStreamAPI(readResponse(3, ""), []byte("abcXY"), nil, 0)
	data, err := readAll(t, api)
	if data != "abc" || err == nil || !strings.Contains(err.Error(), "trailing body") {
		t.Fatalf("extra body = %q, %v", data, err)
	}
	if got := atomic.LoadInt32(&api.inspects); got != 0 {
		t.Fatalf("extra body waited on exec %d times, want zero", got)
	}
	if got := atomic.LoadInt32(&api.closes); got != 1 {
		t.Fatalf("extra body aborted the exec %d times, want exactly one", got)
	}
}

func TestDockerFilesystemReadNonEOFStreamErrorAborts(t *testing.T) {
	// A non-EOF error from the underlying stream must abort (kill) and release
	// every resource rather than leak the exec and pipes.
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	var kills, releases int32
	handle := &dockerclient.ExecHandle{
		Stdout: stdoutR,
		Stderr: stderrR,
		Wait:   func() (int, error) { t.Error("Wait must not run after a stream error"); return 0, nil },
		Kill:   func() error { atomic.AddInt32(&kills, 1); _ = stdoutW.Close(); _ = stderrW.Close(); return nil },
		Release: func() error {
			atomic.AddInt32(&releases, 1)
			return nil
		},
	}
	boom := errors.New("stream boom")
	r := &dockerReadCloser{reader: errReader{err: boom}, handle: handle, stderr: newStderrDrain(stderrR), remaining: 10}
	if _, err := r.Read(make([]byte, 4)); !errors.Is(err, boom) {
		t.Fatalf("read err = %v, want %v", err, boom)
	}
	if atomic.LoadInt32(&kills) != 1 || atomic.LoadInt32(&releases) != 0 {
		t.Fatalf("kills=%d releases=%d, want kill only", kills, releases)
	}
	// Close after an abort is idempotent and does not kill again.
	if err := r.Close(); err != nil {
		t.Fatalf("close after abort: %v", err)
	}
	if atomic.LoadInt32(&kills) != 1 {
		t.Fatalf("kills after close = %d, want 1", kills)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestDockerFilesystemReadShortBody(t *testing.T) {
	// Body frame declares 5 but only 3 arrive before the stream ends.
	api := newStreamAPI(readResponse(5, ""), []byte("abc"), nil, 0)
	_, err := readAll(t, api)
	if err == nil || !strings.Contains(err.Error(), "short body") {
		t.Fatalf("short body err = %v", err)
	}
}

// silentPeerExecAPI hands out a hijacked connection whose peer never reads or
// writes, modelling a helper that hangs. Only ctx cancellation can end an op.
type silentPeerExecAPI struct {
	noopAPI
	creates int32
	mu      sync.Mutex
	peers   []net.Conn
}

func (a *silentPeerExecAPI) ExecCreate(context.Context, string, mobyclient.ExecCreateOptions) (mobyclient.ExecCreateResult, error) {
	atomic.AddInt32(&a.creates, 1)
	return mobyclient.ExecCreateResult{ID: "exec"}, nil
}

func (a *silentPeerExecAPI) ExecAttach(context.Context, string, mobyclient.ExecAttachOptions) (mobyclient.ExecAttachResult, error) {
	client, peer := net.Pipe()
	a.mu.Lock()
	a.peers = append(a.peers, peer) // retain; never read/written
	a.mu.Unlock()
	return mobyclient.ExecAttachResult{HijackedResponse: mobyclient.NewHijackedResponse(client, "application/vnd.docker.raw-stream")}, nil
}

func (a *silentPeerExecAPI) ExecInspect(context.Context, string, mobyclient.ExecInspectOptions) (mobyclient.ExecInspectResult, error) {
	return mobyclient.ExecInspectResult{Running: true}, nil
}

func TestDockerFilesystemWriteCancellationIsOutcomeUnknown(t *testing.T) {
	api := &silentPeerExecAPI{}
	fsys := testDockerFilesystem(api)
	ctx, cancel := context.WithCancel(context.Background())
	length := int64(1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- fsys.Write(ctx, "/workspace/file", strings.NewReader("x"), sandboxpkg.WriteOptions{ContentLength: &length})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if !sandboxpkg.IsOutcomeUnknown(err) {
			t.Fatalf("write cancellation err = %v, want OutcomeUnknown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write did not unblock within 2s of cancellation")
	}
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("ExecCreate called %d times, want exactly one", got)
	}
}

func TestDockerFilesystemReadCancellationReturnsError(t *testing.T) {
	api := &silentPeerExecAPI{}
	fsys := testDockerFilesystem(api)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, _, err := fsys.Read(ctx, "/workspace/file", sandboxpkg.ReadOptions{MaxBytes: 16})
		errCh <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("read returned nil error on cancellation")
		}
		if sandboxpkg.IsOutcomeUnknown(err) {
			t.Fatalf("read cancellation must not claim an unknown mutation outcome: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read did not unblock within 2s of cancellation")
	}
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("ExecCreate called %d times, want exactly one", got)
	}
}

func TestDockerFilesystemStatMapsTypedNotExist(t *testing.T) {
	// The helper's typed not_exist code must reconstruct fs.ErrNotExist on the
	// client so errors.Is behaves identically to the in-process providers.
	resp := fsops.Response{Version: fsops.ProtocolVersion, Kind: fsops.KindStat, Error: "fsops: stat: file does not exist", ErrorCode: fsops.ErrorCodeNotExist}
	payload, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	api := &filesystemExecAPI{response: frameStdout(fsFrame(payload))}
	fsys := testDockerFilesystem(api)
	if _, err := fsys.Stat(context.Background(), "/workspace/absent"); !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("Stat error = %v, want fs.ErrNotExist", err)
	}
}

func TestDockerFilesystemReadLimitBoundary(t *testing.T) {
	// Limit is 2, file is 10: the helper streams exactly the limit and flags
	// read_limit; the client surfaces ErrReadLimit after the bytes.
	api := newStreamAPI(readResponseSized(2, 10, fsops.ErrorCodeReadLimit), []byte("ab"), nil, 0)
	r, _, err := openReadMax(t, api, 2)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(r)
	_ = r.Close()
	if string(data) != "ab" || !errors.Is(readErr, sandboxpkg.ErrReadLimit) {
		t.Fatalf("read-limit boundary = %q, %v", data, readErr)
	}
}

func TestDockerFilesystemReadRejectsOversizedBodyWithoutDeadlock(t *testing.T) {
	// A malicious helper declares a body larger than the requested limit. Decode
	// must reject it before streaming, and teardown must not deadlock on the
	// unread body still queued in the stream.
	for name, resp := range map[string][]byte{
		"body over limit":    readResponseSized(9, 9, ""),                        // BodyLength 9 > MaxBytes 4
		"body over filesize": readResponseSized(4, 2, ""),                        // BodyLength 4 > Info.Size 2
		"bad read_limit":     readResponseSized(4, 4, fsops.ErrorCodeReadLimit),  // Info.Size not > MaxBytes
		"limit body wrong":   readResponseSized(3, 10, fsops.ErrorCodeReadLimit), // BodyLength != MaxBytes
	} {
		t.Run(name, func(t *testing.T) {
			api := newStreamAPI(resp, []byte("abcdefghij"), nil, 0)
			// A rejection returns promptly (no deadlock on the unread body) with a
			// read transport error, and aborts the exec exactly once.
			_, _, err := openReadMax(t, api, 4)
			if err == nil {
				t.Fatal("oversized body was accepted")
			}
			if got := atomic.LoadInt32(&api.closes); got != 1 {
				t.Fatalf("rejection aborted the exec %d times, want exactly one", got)
			}
		})
	}
}

func TestDockerFilesystemReadStderrDrainAndNonzeroExit(t *testing.T) {
	// The stderr frame precedes stdout: without an eager drain the demux would
	// block here and deadlock DecodeResponse. The nonzero exit surfaces after
	// the body, carrying the captured stderr text.
	api := newStreamAPI(readResponse(3, ""), []byte("abc"), []byte("boom"), 1)
	_, err := readAll(t, api)
	if err == nil || !strings.Contains(err.Error(), "exited 1") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("nonzero exit err = %v", err)
	}
}

func TestDockerFilesystemReadWaitFailure(t *testing.T) {
	api := newStreamAPI(readResponse(3, ""), []byte("abc"), nil, 0)
	api.inspectErr = errors.New("inspect exploded")
	_, err := readAll(t, api)
	if err == nil || !strings.Contains(err.Error(), "inspect exploded") {
		t.Fatalf("wait failure err = %v", err)
	}
}

func TestDockerFilesystemReadEarlyCloseKillsOnceAndIsIdempotent(t *testing.T) {
	api := newStreamAPI(readResponse(100, ""), []byte(strings.Repeat("x", 100)), nil, 0)
	r, _, err := openRead(t, api)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if got := atomic.LoadInt32(&api.closes); got != 1 {
		t.Fatalf("early close killed exec %d times, want exactly one", got)
	}
	if got := atomic.LoadInt32(&api.inspects); got != 0 {
		t.Fatalf("early close waited on exec %d times, want zero", got)
	}
}

func openRead(t *testing.T, api dockerclient.API) (io.ReadCloser, sandboxpkg.FileInfo, error) {
	t.Helper()
	return openReadMax(t, api, 1024)
}

func openReadMax(t *testing.T, api dockerclient.API, maxBytes int64) (io.ReadCloser, sandboxpkg.FileInfo, error) {
	t.Helper()
	fs := testDockerFilesystem(api)
	return fs.Read(context.Background(), "/workspace/file", sandboxpkg.ReadOptions{MaxBytes: maxBytes})
}

func readAll(t *testing.T, api dockerclient.API) (string, error) {
	t.Helper()
	r, _, err := openRead(t, api)
	if err != nil {
		return "", err
	}
	data, readErr := io.ReadAll(r)
	_ = r.Close()
	return string(data), readErr
}

// streamExecAPI multiplexes a canned stdout/stderr stream to the streaming exec
// handle and reports a fixed exit code, letting read-lifecycle tests run with no
// daemon. It counts ExecInspect (Wait) and attach Close (Kill or Release)
// invocations; clean release is told from early abort by whether Wait ran.
type streamExecAPI struct {
	noopAPI
	creates    int32
	stream     []byte
	exitCode   int
	inspectErr error
	inspects   int32
	closes     int32
}

func newStreamAPI(response, body, stderr []byte, exitCode int) *streamExecAPI {
	stream := make([]byte, 0, len(response)+len(body)+len(stderr)+16)
	if len(stderr) > 0 {
		stream = append(stream, frameStderr(stderr)...)
	}
	stream = append(stream, frameStdout(response)...)
	if len(body) > 0 {
		stream = append(stream, frameStdout(body)...)
	}
	return &streamExecAPI{stream: stream, exitCode: exitCode}
}

func (a *streamExecAPI) ExecCreate(context.Context, string, mobyclient.ExecCreateOptions) (mobyclient.ExecCreateResult, error) {
	atomic.AddInt32(&a.creates, 1)
	return mobyclient.ExecCreateResult{ID: "exec"}, nil
}

func (a *streamExecAPI) ExecAttach(context.Context, string, mobyclient.ExecAttachOptions) (mobyclient.ExecAttachResult, error) {
	conn := &fakeConn{r: bytes.NewReader(a.stream), closes: &a.closes}
	return mobyclient.ExecAttachResult{HijackedResponse: mobyclient.NewHijackedResponse(conn, "application/vnd.docker.raw-stream")}, nil
}

func (a *streamExecAPI) ExecInspect(context.Context, string, mobyclient.ExecInspectOptions) (mobyclient.ExecInspectResult, error) {
	atomic.AddInt32(&a.inspects, 1)
	if a.inspectErr != nil {
		return mobyclient.ExecInspectResult{}, a.inspectErr
	}
	return mobyclient.ExecInspectResult{Running: false, ExitCode: a.exitCode}, nil
}

// fakeConn models one exec attach: reads serve the canned multiplexed stream
// then EOF, writes discard stdin, and the two directions are independent so
// draining or closing the response side never disturbs a pending stdin write.
// Close is counted so a test can assert Kill fired exactly once.
type fakeConn struct {
	r      *bytes.Reader
	closes *int32
	mu     sync.Mutex
	closed bool
}

func (c *fakeConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, io.EOF
	}
	return c.r.Read(p)
}

func (c *fakeConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		atomic.AddInt32(c.closes, 1)
	}
	return nil
}
func (c *fakeConn) LocalAddr() net.Addr                { return fakeAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr               { return fakeAddr{} }
func (c *fakeConn) SetDeadline(t time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(t time.Time) error { return nil }

type fakeAddr struct{}

func (fakeAddr) Network() string { return "fake" }
func (fakeAddr) String() string  { return "fake" }

func testDockerFilesystem(api dockerclient.API) *dockerFilesystem {
	return &dockerFilesystem{session: &dockerSession{client: dockerclient.NewWithAPI(api), containerID: "container", mountTable: []dockerclient.Mount{{ContainerPath: sandboxpkg.PathWorkspace}}}}
}

// --- framing helpers ---

// stdcopyFrame prepends the 8-byte Docker stdcopy multiplexing header.
func stdcopyFrame(stream byte, payload []byte) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, payload...)
}
func frameStdout(payload []byte) []byte { return stdcopyFrame(1, payload) }
func frameStderr(payload []byte) []byte { return stdcopyFrame(2, payload) }

// fsFrame wraps a helper payload in the 4-byte length-prefixed protocol frame.
func fsFrame(payload []byte) []byte {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(payload)))
	return append(header, payload...)
}

// readResponse builds a consistent read reply whose declared body equals the
// reported file size (the ordinary, non-truncated case).
func readResponse(bodyLen int64, code string) []byte {
	return readResponseSized(bodyLen, bodyLen, code)
}

// readResponseSized builds a read reply with an explicit file size, letting a
// test forge read-limit (fileSize > bodyLen) or contradictory bounds.
func readResponseSized(bodyLen, fileSize int64, code string) []byte {
	resp := fsops.Response{Version: fsops.ProtocolVersion, Kind: fsops.KindRead, Info: sandboxpkg.FileInfo{Name: "file", Size: fileSize}, BodyLength: bodyLen, ErrorCode: code}
	payload, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	return fsFrame(payload)
}

func TestDockerfileInstallsDigestMatchedFilesystemHelper(t *testing.T) {
	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "COPY --from=stella-builder /out/stellad /opt/stella/bin/stella-fs") {
		t.Fatal("Dockerfile must install stella-fs from the same stellad build stage")
	}
}

func TestDockerfileLabelsHelperRevisionFromVersion(t *testing.T) {
	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	src := string(dockerfile)
	// The helper binary is compiled with VERSION, so the runtime stage must
	// declare that arg and derive the fs-helper-revision label from it.
	for _, want := range []string{
		"ARG VERSION",
		"LABEL org.cherryhq.stella.fs-helper-revision=${VERSION}",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("Dockerfile runtime stage must contain %q", want)
		}
	}
	// The label the runtime stage bakes must match what preflight looks up.
	if fsHelperRevisionLabel != "org.cherryhq.stella.fs-helper-revision" {
		t.Fatalf("preflight label %q drifted from the Dockerfile", fsHelperRevisionLabel)
	}
}
