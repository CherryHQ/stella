package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDisplayVersion(t *testing.T) {
	prev := version
	t.Cleanup(func() { version = prev })

	version = "v1.2.3"
	if got := displayVersion(); got != "1.2.3" {
		t.Fatalf("displayVersion() = %q, want %q", got, "1.2.3")
	}

	version = ""
	if got := displayVersion(); got != "dev" {
		t.Fatalf("displayVersion() = %q, want %q", got, "dev")
	}
}

func TestSelectReleaseAsset(t *testing.T) {
	assets := []githubReleaseAsset{
		{Name: "anna_0.3.0_darwin_arm64.tar.gz"},
		{Name: "anna_0.3.0_linux_amd64.tar.gz"},
	}

	asset, err := selectReleaseAsset(assets, "darwin", "arm64")
	if err != nil {
		t.Fatalf("selectReleaseAsset() error = %v", err)
	}
	if asset.Name != "anna_0.3.0_darwin_arm64.tar.gz" {
		t.Fatalf("selectReleaseAsset() = %q, want darwin arm64 asset", asset.Name)
	}
}

func TestSelectReleaseAssetUnsupported(t *testing.T) {
	_, err := selectReleaseAsset([]githubReleaseAsset{{Name: "anna_0.3.0_linux_amd64.tar.gz"}}, "darwin", "arm64")
	if !errors.Is(err, errUnsupportedAsset) {
		t.Fatalf("selectReleaseAsset() error = %v, want errUnsupportedAsset", err)
	}
}

func TestFetchLatestRelease(t *testing.T) {
	prevBaseURL := upgradeAPIBaseURL
	prevClient := upgradeHTTPClient
	t.Cleanup(func() {
		upgradeAPIBaseURL = prevBaseURL
		upgradeHTTPClient = prevClient
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/vaayne/anna/releases/latest" {
			t.Fatalf("path = %s, want /repos/vaayne/anna/releases/latest", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.3.0","assets":[{"name":"anna_0.3.0_darwin_arm64.tar.gz","browser_download_url":"https://example.com/anna"}]}`))
	}))
	defer server.Close()

	upgradeAPIBaseURL = server.URL
	upgradeHTTPClient = server.Client()

	release, err := fetchLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("fetchLatestRelease() error = %v", err)
	}
	if release.TagName != "v0.3.0" {
		t.Fatalf("fetchLatestRelease() tag = %q, want v0.3.0", release.TagName)
	}
	if len(release.Assets) != 1 {
		t.Fatalf("fetchLatestRelease() assets = %d, want 1", len(release.Assets))
	}
}

func TestRunUpgradeAlreadyCurrent(t *testing.T) {
	prevBaseURL := upgradeAPIBaseURL
	prevClient := upgradeHTTPClient
	t.Cleanup(func() {
		upgradeAPIBaseURL = prevBaseURL
		upgradeHTTPClient = prevClient
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.3.0","assets":[]}`))
	}))
	defer server.Close()

	upgradeAPIBaseURL = server.URL
	upgradeHTTPClient = server.Client()

	result, err := runUpgrade(context.Background(), t.TempDir(), "0.3.0", "darwin", "arm64")
	if err != nil {
		t.Fatalf("runUpgrade() error = %v", err)
	}
	if !result.AlreadyCurrent {
		t.Fatalf("runUpgrade() AlreadyCurrent = false, want true")
	}
	if result.TargetPath != "" {
		t.Fatalf("runUpgrade() TargetPath = %q, want empty", result.TargetPath)
	}
}

func TestRunUpgradeInstallsBinary(t *testing.T) {
	prevBaseURL := upgradeAPIBaseURL
	prevClient := upgradeHTTPClient
	t.Cleanup(func() {
		upgradeAPIBaseURL = prevBaseURL
		upgradeHTTPClient = prevClient
	})

	archivePath := filepath.Join(t.TempDir(), "anna_0.4.0_darwin_arm64.tar.gz")
	if err := writeTarGzArchive(archivePath, "anna", []byte("new-binary")); err != nil {
		t.Fatalf("writeTarGzArchive() error = %v", err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/vaayne/anna/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"tag_name":"v0.4.0","assets":[{"name":"anna_0.4.0_darwin_arm64.tar.gz","browser_download_url":"%s/download/anna_0.4.0_darwin_arm64.tar.gz"}]}`, server.URL)
		case "/download/anna_0.4.0_darwin_arm64.tar.gz":
			http.ServeFile(w, r, archivePath)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	upgradeAPIBaseURL = server.URL
	upgradeHTTPClient = server.Client()

	installDir := t.TempDir()
	result, err := runUpgrade(context.Background(), installDir, "0.3.0", "darwin", "arm64")
	if err != nil {
		t.Fatalf("runUpgrade() error = %v", err)
	}
	if result.AlreadyCurrent {
		t.Fatalf("runUpgrade() AlreadyCurrent = true, want false")
	}

	data, err := os.ReadFile(filepath.Join(installDir, "anna"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "new-binary" {
		t.Fatalf("installed binary = %q, want new-binary", string(data))
	}
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "anna.tar.gz")
	if err := writeTarGzArchive(archivePath, "anna", []byte("tar-binary")); err != nil {
		t.Fatalf("writeTarGzArchive() error = %v", err)
	}

	extractedPath, err := extractBinaryFromTarGz(archivePath, dir, "anna")
	if err != nil {
		t.Fatalf("extractBinaryFromTarGz() error = %v", err)
	}

	data, err := os.ReadFile(extractedPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "tar-binary" {
		t.Fatalf("extractBinaryFromTarGz() data = %q, want tar-binary", string(data))
	}
}

func TestExtractBinaryFromZip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "anna.zip")
	if err := writeZipArchive(archivePath, "anna.exe", []byte("zip-binary")); err != nil {
		t.Fatalf("writeZipArchive() error = %v", err)
	}

	extractedPath, err := extractBinaryFromZip(archivePath, dir, "anna.exe")
	if err != nil {
		t.Fatalf("extractBinaryFromZip() error = %v", err)
	}

	data, err := os.ReadFile(extractedPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "zip-binary" {
		t.Fatalf("extractBinaryFromZip() data = %q, want zip-binary", string(data))
	}
}

func TestInstallBinaryRestoresExistingOnRenameFailure(t *testing.T) {
	prevRename := renameFile
	t.Cleanup(func() { renameFile = prevRename })

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "anna-new")
	targetPath := filepath.Join(dir, "anna")
	if err := os.WriteFile(srcPath, []byte("new"), 0o755); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}

	failInstall := true
	renameFile = func(oldPath, newPath string) error {
		if filepath.Base(oldPath) == "anna.tmp" && filepath.Base(newPath) == "anna" && failInstall {
			failInstall = false
			return errors.New("simulated rename failure")
		}
		return os.Rename(oldPath, newPath)
	}

	err := installBinary(srcPath, targetPath, true)
	if err == nil {
		t.Fatal("installBinary() error = nil, want rename failure")
	}

	data, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("ReadFile(target) error = %v", readErr)
	}
	if string(data) != "old" {
		t.Fatalf("target content = %q, want old", string(data))
	}
}

func TestInstallBinaryRejectsDirectoryTarget(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "anna-new")
	targetPath := filepath.Join(dir, "anna")
	if err := os.WriteFile(srcPath, []byte("new"), 0o755); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	if err := os.Mkdir(targetPath, 0o755); err != nil {
		t.Fatalf("Mkdir(target) error = %v", err)
	}

	err := installBinary(srcPath, targetPath, true)
	if !errors.Is(err, errInvalidTarget) {
		t.Fatalf("installBinary() error = %v, want errInvalidTarget", err)
	}
}

func writeTarGzArchive(path, name string, data []byte) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}

	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)

	if err := tarWriter.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(data)),
	}); err != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		_ = file.Close()
		return err
	}
	if _, err := tarWriter.Write(data); err != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		_ = file.Close()
		return err
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		_ = file.Close()
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func writeZipArchive(path, name string, data []byte) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}

	zipWriter := zip.NewWriter(file)
	writer, err := zipWriter.Create(name)
	if err != nil {
		_ = zipWriter.Close()
		_ = file.Close()
		return err
	}
	if _, err := writer.Write(data); err != nil {
		_ = zipWriter.Close()
		_ = file.Close()
		return err
	}
	if err := zipWriter.Close(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
