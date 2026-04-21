package builddeps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type errReader struct {
	data []byte
	read bool
}

func (r *errReader) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		return copy(p, r.data), nil
	}
	return 0, errors.New("boom")
}

func (r *errReader) Close() error { return nil }

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDownloadRemovesPartialFileOnCopyError(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &errReader{data: []byte("hello")},
			Header:     make(http.Header),
		}, nil
	})}
	dest := filepath.Join(t.TempDir(), "tool.tar.gz")
	err := Download(context.Background(), client, "https://example.com/tool.tar.gz", dest)
	if err == nil {
		t.Fatal("expected download error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("partial destination should not exist, stat err = %v", statErr)
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected temp download artifacts to be cleaned up, found %d entries", len(entries))
	}
}

func TestDownloadRenamesCompletedTempFile(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})}
	dest := filepath.Join(t.TempDir(), "tool.tar.gz")
	if err := Download(context.Background(), client, "https://example.com/tool.tar.gz", dest); err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("Download() wrote %q, want ok", string(data))
	}
}
