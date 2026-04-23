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

func TestFetchLatestVersion(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://api.github.com/repos/jdx/mise/releases/latest" {
			t.Fatalf("unexpected URL: %s", req.URL.String())
		}
		if got := req.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Fatalf("unexpected Accept header: %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"tag_name":"v2025.4.0"}`)),
			Header:     make(http.Header),
		}, nil
	})}
	version, err := FetchLatestVersion(context.Background(), client, "jdx/mise")
	if err != nil {
		t.Fatalf("FetchLatestVersion() error = %v", err)
	}
	if version != "2025.4.0" {
		t.Fatalf("FetchLatestVersion() = %q, want 2025.4.0", version)
	}
}

func TestGitHubReleaseAssetURL(t *testing.T) {
	if got := GitHubReleaseAssetURL("", "cli/cli", "v2.75.0", "gh.tar.gz"); got != "https://github.com/cli/cli/releases/download/v2.75.0/gh.tar.gz" {
		t.Fatalf("unexpected default asset URL: %s", got)
	}
	if got := GitHubReleaseAssetURL("https://example.com/", "cli/cli", "v2.75.0", "gh.tar.gz"); got != "https://example.com/cli/cli/releases/download/v2.75.0/gh.tar.gz" {
		t.Fatalf("unexpected custom asset URL: %s", got)
	}
}
