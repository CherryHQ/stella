package builddeps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Download downloads url to destPath using the provided client.
func Download(ctx context.Context, client *http.Client, url, destPath string) error {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %s: %w", destPath, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".download-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", destPath, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", destPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", destPath, err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("rename %s: %w", destPath, err)
	}
	return nil
}

// GitHubReleaseAssetURL builds the direct release asset URL for repo/tag/file.
func GitHubReleaseAssetURL(repo, tag, file string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, file)
}
