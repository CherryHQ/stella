package fsops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// Provider-neutral conformance for the raw fsops.Filesystem lives in the shared
// fstest suite (see conformance_test.go, "library" case). The tests below cover
// fsops-internal concerns the suite deliberately does not: mount validation,
// write non-retry, and the helper wire protocol.

func TestFilesystemRejectsEscapesButResolvesContainedLinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real"), []byte("yes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	f, err := NewFilesystem([]Mount{{Path: sandbox.PathWorkspace, Directory: root}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	// Escaping paths — a lexical escape and one through an escaping symlink — fail
	// closed, but a symlink resolving inside the mount is followed (POSIX).
	for _, p := range []string{"/workspace/../outside", "/workspace/escape/secret"} {
		if _, err := f.Stat(context.Background(), p); err == nil {
			t.Fatalf("Stat(%q) succeeded", p)
		}
	}
	if info, err := f.Stat(context.Background(), "/workspace/alias"); err != nil || info.IsDir {
		t.Fatalf("contained symlink did not resolve: %#v, %v", info, err)
	}
}

func TestFilesystemRejectsReadOnlyAndDuplicateMounts(t *testing.T) {
	root := t.TempDir()
	if _, err := NewFilesystem([]Mount{{Path: sandbox.PathWorkspace, Directory: root}, {Path: sandbox.PathWorkspace, Directory: root}}); err == nil {
		t.Fatal("accepted duplicate mount")
	}
	f, err := NewFilesystem([]Mount{{Path: sandbox.PathWorkspace, Directory: root, ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := f.Write(context.Background(), "/workspace/file", strings.NewReader("no"), sandbox.WriteOptions{}); err == nil {
		t.Fatal("wrote read-only mount")
	}
}

func TestFilesystemDoesNotRetryWrite(t *testing.T) {
	root := t.TempDir()
	f, err := NewFilesystem([]Mount{{Path: sandbox.PathWorkspace, Directory: root}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	reader := &failingReader{calls: 0}
	err = f.Write(context.Background(), "/workspace/write", reader, sandbox.WriteOptions{})
	if err == nil || reader.calls != 1 {
		t.Fatalf("err = %v, reader calls = %d", err, reader.calls)
	}
}

func TestDecodeResponseRejectsCrossKindAndImpossibleShapes(t *testing.T) {
	frame := func(js string) *bytes.Buffer {
		var b bytes.Buffer
		if err := writeFrame(&b, []byte(js)); err != nil {
			t.Fatal(err)
		}
		return &b
	}
	// A stat-shaped reply must not satisfy a read request.
	if _, err := DecodeResponse(frame(`{"version":1,"kind":"stat","info":{"Name":"f"}}`), KindRead); err == nil {
		t.Fatal("accepted stat reply for a read request")
	}
	// info+entries+body together fits no kind and must fail closed.
	impossible := `{"version":1,"kind":"read","info":{"Name":"f"},"entries":[{"Name":"e"}],"body_length":3}`
	if _, err := DecodeResponse(frame(impossible), KindRead); err == nil {
		t.Fatal("accepted impossible info+entries+body reply")
	}
	// An empty-list success is a valid list, but must be disjoint from a mutation.
	if _, err := DecodeResponse(frame(`{"version":1,"kind":"list"}`), KindList); err != nil {
		t.Fatalf("rejected a valid empty-list reply: %v", err)
	}
	if _, err := DecodeResponse(frame(`{"version":1,"kind":"list"}`), KindMutation); err == nil {
		t.Fatal("accepted an empty-list reply for a mutation request")
	}
}

func TestErrorCodeClassificationAndMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
		is   error // sentinel a remote client must recover, nil for opaque
	}{
		{"not_exist", fs.ErrNotExist, ErrorCodeNotExist, fs.ErrNotExist},
		{"permission", fs.ErrPermission, ErrorCodePermission, fs.ErrPermission},
		{"exist", fs.ErrExist, ErrorCodeExist, fs.ErrExist},
		{"read_limit", sandbox.ErrReadLimit, ErrorCodeReadLimit, sandbox.ErrReadLimit},
		{"opaque", errors.New("some driver failure"), ErrorCodeOpaque, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Helper classifies the io/fs sentinel (even when wrapped) to a code.
			if got := classifyErrorCode(fmt.Errorf("fsops: stat: %w", c.err)); got != c.code {
				t.Fatalf("classify = %q, want %q", got, c.code)
			}
			// Client maps the code back so errors.Is matches the in-process error.
			mapped := ResponseError(Response{Kind: KindStat, ErrorCode: c.code, Error: "boom"})
			if c.is != nil && !errors.Is(mapped, c.is) {
				t.Fatalf("ResponseError(%q) = %v, want Is %v", c.code, mapped, c.is)
			}
			if c.is == nil && mapped == nil {
				t.Fatal("opaque error must still surface a message")
			}
		})
	}
}

func TestDecodeResponseRejectsUnknownErrorCode(t *testing.T) {
	var b bytes.Buffer
	if err := writeFrame(&b, []byte(`{"version":1,"kind":"stat","error":"x","error_code":"teleport"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeResponse(&b, KindStat); err == nil || !strings.Contains(err.Error(), "unknown helper error code") {
		t.Fatalf("unknown code accepted: %v", err)
	}
}

func TestHelperClassifiesMissingPathAsNotExist(t *testing.T) {
	root := t.TempDir()
	request, err := EncodeRequest(Request{Version: ProtocolVersion, Operation: "stat", Path: "absent"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Serve(context.Background(), root, bytes.NewReader(request), &out); err != nil {
		t.Fatal(err)
	}
	response, err := DecodeResponse(&out, KindStat)
	if err != nil {
		t.Fatal(err)
	}
	if response.ErrorCode != ErrorCodeNotExist {
		t.Fatalf("code = %q, want %q", response.ErrorCode, ErrorCodeNotExist)
	}
	if !errors.Is(ResponseError(response), fs.ErrNotExist) {
		t.Fatalf("mapped error not fs.ErrNotExist: %v", ResponseError(response))
	}
}

func TestHelperRejectsMalformedFrames(t *testing.T) {
	var out bytes.Buffer
	if err := Serve(context.Background(), t.TempDir(), bytes.NewBuffer([]byte{0, 0, 0, 2, '{', ']'}), &out); err == nil {
		t.Fatal("Serve accepted malformed JSON")
	}
	if err := Serve(context.Background(), t.TempDir(), bytes.NewBuffer([]byte{0, 16, 0, 1}), &out); err == nil {
		t.Fatal("Serve accepted oversized frame")
	}
}

func TestHelperSingleWrite(t *testing.T) {
	root := t.TempDir()
	request, err := EncodeRequest(Request{Version: ProtocolVersion, Operation: "write", Path: "file", BodyLength: 2})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Serve(context.Background(), root, io.MultiReader(bytes.NewReader(request), strings.NewReader("ok")), &out); err != nil {
		t.Fatal(err)
	}
	response, err := DecodeResponse(&out, KindMutation)
	if err != nil || response.Error != "" {
		t.Fatalf("response = %#v, %v", response, err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "file")); err != nil || string(got) != "ok" {
		t.Fatalf("written file = %q, %v", got, err)
	}
}

func TestHelperStreamsDeclaredBodyAndRejectsProtocolDrift(t *testing.T) {
	root := t.TempDir()
	const size = 2 << 20
	request, err := EncodeRequest(Request{Version: ProtocolVersion, Operation: "upload", Path: "large", BodyLength: size})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Serve(context.Background(), root, io.MultiReader(bytes.NewReader(request), strings.NewReader(strings.Repeat("x", size))), &out); err != nil {
		t.Fatal(err)
	}
	response, err := DecodeResponse(&out, KindMutation)
	if err != nil || response.Error != "" {
		t.Fatalf("response = %#v, %v", response, err)
	}
	info, err := os.Stat(filepath.Join(root, "large"))
	if err != nil || info.Size() != size {
		t.Fatalf("size = %d, %v", info.Size(), err)
	}
	// Unknown metadata must fail before any operation is attempted.
	var raw bytes.Buffer
	if err := writeFrame(&raw, []byte(`{"version":1,"operation":"stat","unknown":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := Serve(context.Background(), root, &raw, io.Discard); err == nil {
		t.Fatal("helper accepted unknown metadata")
	}
}

func TestHelperRejectsShortAndExtraBodies(t *testing.T) {
	for name, body := range map[string]string{"short": "x", "extra": "xyz"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			request, err := EncodeRequest(Request{Version: ProtocolVersion, Operation: "write", Path: "file", BodyLength: 2})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := Serve(context.Background(), root, io.MultiReader(bytes.NewReader(request), strings.NewReader(body)), &out); err != nil {
				t.Fatal(err)
			}
			response, err := DecodeResponse(&out, KindMutation)
			if err != nil || !strings.Contains(response.Error, "helper body") {
				t.Fatalf("response = %#v, %v", response, err)
			}
			got, readErr := os.ReadFile(filepath.Join(root, "file"))
			want := body[:min(len(body), 2)]
			if readErr != nil || string(got) != want {
				t.Fatalf("partial file = %q, %v; want %q", got, readErr, want)
			}
		})
	}
}

type failingReader struct{ calls int }

func (r *failingReader) Read([]byte) (int, error) { r.calls++; return 0, errors.New("interrupted") }
