package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// infiniteZeroReader yields an endless stream of zero bytes without allocating,
// so size-limit tests can drive past maxArchiveBytes cheaply.
type infiniteZeroReader struct{}

func (infiniteZeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestResolveUpgradeDirDefaultUsesExecutableDir(t *testing.T) {
	tmpDir := t.TempDir()
	exePath := filepath.Join(tmpDir, "stellad")
	if err := os.WriteFile(exePath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return exePath, nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	got, err := resolveUpgradeDir("")
	if err != nil {
		t.Fatalf("resolveUpgradeDir: %v", err)
	}
	want, _ := filepath.EvalSymlinks(tmpDir)
	if got != want {
		t.Fatalf("resolveUpgradeDir() = %q, want %q", got, want)
	}
}

func TestResolveUpgradeDirWithInstallDir(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "usr", "local", "bin")
	got, err := resolveUpgradeDir(dir)
	if err != nil {
		t.Fatalf("resolveUpgradeDir: %v", err)
	}
	if got != dir {
		t.Fatalf("resolveUpgradeDir() = %q, want %q", got, dir)
	}
}

func TestResolveUpgradeDirReportsExecutablePathError(t *testing.T) {
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return "", errors.New("boom") }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	_, err := resolveUpgradeDir("")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBinariesToUpgrade(t *testing.T) {
	got := binariesToUpgrade("linux")
	if len(got) != 1 || got[0] != "stellad" {
		t.Fatalf("linux: got %v, want [stellad]", got)
	}
	got = binariesToUpgrade("windows")
	if len(got) != 1 || got[0] != "stellad.exe" {
		t.Fatalf("windows: got %v, want [stellad.exe]", got)
	}
}

func TestUpgradeInstallErrorAddsPermissionHint(t *testing.T) {
	err := upgradeInstallError(errors.New("rename stellad.tmp stellad: permission denied"), "/usr/local/bin/stellad", "linux")

	if !strings.Contains(err.Error(), "permission denied replacing /usr/local/bin/stellad") {
		t.Fatalf("error = %q, want permission hint", err.Error())
	}
}

func TestUpgradeInstallErrorAddsWindowsBusyHint(t *testing.T) {
	err := upgradeInstallError(errors.New("The process cannot access the file because it is being used by another process."), `C:\\bin\\stellad.exe`, "windows")

	if !strings.Contains(err.Error(), "locked by a running process") {
		t.Fatalf("error = %q, want busy hint", err.Error())
	}
}

func TestCleanStaleUpgradeArtifacts(t *testing.T) {
	dir := t.TempDir()

	// Create stale files that should be removed.
	staleFiles := []string{
		"stellad.tmp",
		"stellad.old",
		"stellad.exe.tmp",
	}
	for _, name := range staleFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Create files that should NOT be removed. The .bak files are kept because
	// their targets exist: a backup surviving next to its target means a
	// rollback could not restore it, so it is preserved for manual recovery.
	keepFiles := []string{
		"stellad", "stellad.exe",
		"unrelated.tmp", "other.bak",
		"stellad.bak", "stellad.exe.bak",
	}
	for _, name := range keepFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cleanStaleUpgradeArtifacts(dir)

	for _, name := range staleFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, but it still exists", name)
		}
	}
	for _, name := range keepFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to be kept, but it was removed", name)
		}
	}
}

func TestCleanStaleUpgradeArtifactsRestoresBackupWhenTargetMissing(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, "stellad.bak")
	if err := os.WriteFile(backup, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}

	cleanStaleUpgradeArtifacts(dir)

	restored := filepath.Join(dir, "stellad")
	data, err := os.ReadFile(restored)
	if err != nil {
		t.Fatalf("expected backup to be restored: %v", err)
	}
	if string(data) != "original" {
		t.Fatalf("restored content = %q, want original", string(data))
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatal("expected backup path to be gone after restore")
	}
}

func TestCleanStaleUpgradeArtifactsNonexistentDir(t *testing.T) {
	// Should not panic on a nonexistent directory.
	cleanStaleUpgradeArtifacts(filepath.Join(t.TempDir(), "nope"))
}

func TestIsStaleUpgradeArtifact(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"stella.tmp", false},
		{"stellad.bak", true},
		{"stella.old", false},
		{"stellad.exe.tmp", true},
		{"stella.exe.bak", false},
		{"stellad.exe.old", true},
		{"stella", false},
		{"stellad", false},
		{"unrelated.tmp", false},
		{"stellar.bak", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStaleUpgradeArtifact(tt.name); got != tt.want {
				t.Errorf("isStaleUpgradeArtifact(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestRollbackUpgradeRemoveFailureFallsBack(t *testing.T) {
	dir := t.TempDir()

	committed := filepath.Join(dir, "stellad")
	if err := os.WriteFile(committed, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	bakPath := committed + ".bak"
	if err := os.WriteFile(bakPath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Mock removeFile to always fail.
	oldRemoveFile := removeFile
	removeFile = func(name string) error { return errors.New("simulated lock") }
	oldGOOS := currentGOOS
	currentGOOS = "linux"
	t.Cleanup(func() {
		removeFile = oldRemoveFile
		currentGOOS = oldGOOS
	})

	rollbackUpgrade([]string{committed}, []string{committed})

	// The committed file should still exist (remove failed).
	if _, err := os.Stat(committed); err != nil {
		t.Fatal("committed file should still exist after failed remove")
	}
	// The backup should also still exist (rollback skipped restore for this path).
	if _, err := os.Stat(bakPath); err != nil {
		t.Fatal("backup should still exist because remove failed")
	}
}

func TestRollbackUpgradeWindowsRenamesToOld(t *testing.T) {
	dir := t.TempDir()

	committed := filepath.Join(dir, "stellad.exe")
	if err := os.WriteFile(committed, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	bakPath := committed + ".bak"
	if err := os.WriteFile(bakPath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Mock removeFile to fail (simulating Windows file lock),
	// but allow renameFile to succeed (renaming to .old).
	oldRemoveFile := removeFile
	removeFile = func(name string) error { return errors.New("file locked") }
	oldGOOS := currentGOOS
	currentGOOS = "windows"
	t.Cleanup(func() {
		removeFile = oldRemoveFile
		currentGOOS = oldGOOS
	})

	rollbackUpgrade([]string{committed}, []string{committed})

	// The committed file should have been renamed to .old.
	oldPath := committed + ".old"
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatal("expected .old file to exist after Windows rollback rename")
	}

	// Since rename-to-.old succeeded, removeFailed is NOT set, so the backup
	// restore loop runs renameFile(bakPath, committed) — restoring the original.
	data, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("expected backup to be restored at %s: %v", committed, err)
	}
	if string(data) != "original" {
		t.Fatalf("restored file = %q, want %q", string(data), "original")
	}

	// The .bak file should be gone (renamed back).
	if _, err := os.Stat(bakPath); !os.IsNotExist(err) {
		t.Fatal("expected .bak file to be gone after successful restore")
	}
}

func TestParseChecksumForFile(t *testing.T) {
	checksums := "abc123  stella_linux_amd64.tar.gz\ndef456  stella_darwin_arm64.tar.gz\n"

	got, err := parseChecksumForFile(checksums, "stella_linux_amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc123" {
		t.Fatalf("got %q, want %q", got, "abc123")
	}

	_, err = parseChecksumForFile(checksums, "nonexistent.tar.gz")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestSha256File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	content := []byte("hello world")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := sha256File(path)
	if err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(content)
	want := hex.EncodeToString(h[:])
	if got != want {
		t.Fatalf("sha256File = %q, want %q", got, want)
	}
}

func TestWriteReaderToFileSizeLimit(t *testing.T) {
	dir := t.TempDir()

	// Happy path: a normal small file is written verbatim.
	smallPath := filepath.Join(dir, "small.bin")
	if err := writeReaderToFile(smallPath, strings.NewReader("small content"), 0o755); err != nil {
		t.Fatalf("writeReaderToFile: %v", err)
	}
	data, err := os.ReadFile(smallPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "small content" {
		t.Fatalf("got %q, want %q", string(data), "small content")
	}

	if testing.Short() {
		t.Skip("skipping multi-hundred-MB boundary cases in -short mode")
	}

	// Boundary: a payload of exactly maxArchiveBytes is accepted (the guard is
	// inclusive — it must not reject a file at the limit).
	atLimitPath := filepath.Join(dir, "at_limit.bin")
	if err := writeReaderToFile(atLimitPath, io.LimitReader(infiniteZeroReader{}, maxArchiveBytes), 0o755); err != nil {
		t.Fatalf("writeReaderToFile at limit: %v", err)
	}
	if fi, err := os.Stat(atLimitPath); err != nil {
		t.Fatal(err)
	} else if fi.Size() != maxArchiveBytes {
		t.Fatalf("at-limit size = %d, want %d", fi.Size(), maxArchiveBytes)
	}

	// Over limit: one byte past the cap is rejected and the partial file removed.
	overPath := filepath.Join(dir, "over.bin")
	err = writeReaderToFile(overPath, io.LimitReader(infiniteZeroReader{}, maxArchiveBytes+1), 0o755)
	if err == nil {
		t.Fatal("expected over-limit reader to be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %q, want size-limit error", err.Error())
	}
	if _, err := os.Stat(overPath); !os.IsNotExist(err) {
		t.Fatal("expected partial file to be removed after over-limit rejection")
	}
}

func TestExtractBinaryFromTarGzRejectsOversizedSkippedEntry(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "stella_linux_amd64.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gz)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "padding.bin", Mode: 0o644, Size: maxArchiveBytes + 1}); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = extractBinaryFromTarGz(archivePath, dir, "stella")
	if err == nil {
		t.Fatal("expected oversized skipped entry to fail")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %q, want size-limit error", err.Error())
	}
}

func TestFindChecksumAsset(t *testing.T) {
	assets := []githubReleaseAsset{
		{Name: "stella_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/archive"},
		{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums"},
	}

	got, ok := findChecksumAsset(assets)
	if !ok {
		t.Fatal("expected to find checksums.txt")
	}
	if got.Name != "checksums.txt" {
		t.Fatalf("got %q, want checksums.txt", got.Name)
	}

	got, ok = findChecksumAsset([]githubReleaseAsset{{Name: "stella_0.43.1_checksums.txt"}})
	if !ok {
		t.Fatal("expected to find prefixed GoReleaser checksums asset")
	}
	if got.Name != "stella_0.43.1_checksums.txt" {
		t.Fatalf("got %q, want stella_0.43.1_checksums.txt", got.Name)
	}

	_, ok = findChecksumAsset([]githubReleaseAsset{{Name: "archive.tar.gz"}})
	if ok {
		t.Fatal("expected not found when checksums.txt missing")
	}
}

func TestFetchRelease(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/CherryHQ/stella/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tag_name":"v9.9.9"}`)
	})
	mux.HandleFunc("/repos/CherryHQ/stella/releases/tags/v1.2.3", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tag_name":"v1.2.3"}`)
	})
	mux.HandleFunc("/repos/CherryHQ/stella/releases/tags/v0.0.0", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	prev := upgradeAPIBaseURL
	upgradeAPIBaseURL = srv.URL
	t.Cleanup(func() { upgradeAPIBaseURL = prev })

	t.Run("empty version fetches latest", func(t *testing.T) {
		rel, err := fetchRelease(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if rel.TagName != "v9.9.9" {
			t.Fatalf("tag = %q, want v9.9.9", rel.TagName)
		}
	})

	t.Run("specific version fetches that tag", func(t *testing.T) {
		rel, err := fetchRelease(context.Background(), "1.2.3")
		if err != nil {
			t.Fatal(err)
		}
		if rel.TagName != "v1.2.3" {
			t.Fatalf("tag = %q, want v1.2.3", rel.TagName)
		}
	})

	t.Run("missing version reports a helpful error", func(t *testing.T) {
		_, err := fetchRelease(context.Background(), "0.0.0")
		if err == nil || !strings.Contains(err.Error(), "v0.0.0 not found") {
			t.Fatalf("error = %v, want not-found message", err)
		}
	})
}

func TestDownloadFile(t *testing.T) {
	content := []byte(strings.Repeat("stella", 5000))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "archive.bin")
	var out strings.Builder // not an *os.File, so the progress bar stays silent
	if err := downloadFile(context.Background(), &out, srv.URL, dest); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(content))
	}
	if out.Len() != 0 {
		t.Fatalf("expected no progress output to a non-terminal writer, got %q", out.String())
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{150 << 20, "150.0 MiB"},
		{1 << 30, "1.0 GiB"},
	}
	for _, tt := range tests {
		if got := humanBytes(tt.n); got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestVerifyChecksumIntegration(t *testing.T) {
	const archiveName = "stella_linux_amd64.tar.gz"
	content := []byte("fake archive content")
	h := sha256.Sum256(content)
	correctHash := hex.EncodeToString(h[:])

	// serveChecksums spins an HTTP server returning the given checksums body and
	// builds a release whose checksums.txt asset points at it.
	releaseServing := func(t *testing.T, body string) *githubRelease {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		t.Cleanup(srv.Close)
		return &githubRelease{Assets: []githubReleaseAsset{
			{Name: archiveName, BrowserDownloadURL: srv.URL + "/archive"},
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL},
		}}
	}

	writeArchive := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), archiveName)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("matching checksum passes", func(t *testing.T) {
		rel := releaseServing(t, fmt.Sprintf("%s  %s\n", correctHash, archiveName))
		if err := verifyChecksum(context.Background(), rel, archiveName, writeArchive(t)); err != nil {
			t.Fatalf("verifyChecksum: %v", err)
		}
	})

	t.Run("uppercase digest passes (case-insensitive)", func(t *testing.T) {
		rel := releaseServing(t, fmt.Sprintf("%s  %s\n", strings.ToUpper(correctHash), archiveName))
		if err := verifyChecksum(context.Background(), rel, archiveName, writeArchive(t)); err != nil {
			t.Fatalf("verifyChecksum with uppercase digest: %v", err)
		}
	})

	t.Run("wrong checksum is rejected", func(t *testing.T) {
		bad := strings.Repeat("0", 64)
		rel := releaseServing(t, fmt.Sprintf("%s  %s\n", bad, archiveName))
		err := verifyChecksum(context.Background(), rel, archiveName, writeArchive(t))
		if err == nil || !strings.Contains(err.Error(), "mismatch") {
			t.Fatalf("error = %v, want checksum mismatch", err)
		}
	})

	t.Run("missing checksums.txt asset skips verification", func(t *testing.T) {
		rel := &githubRelease{Assets: []githubReleaseAsset{{Name: archiveName, BrowserDownloadURL: "http://unused"}}}
		if err := verifyChecksum(context.Background(), rel, archiveName, writeArchive(t)); err != nil {
			t.Fatalf("expected missing checksums.txt to skip, got %v", err)
		}
	})
}

func TestWarnStaleUpgradeArtifacts(t *testing.T) {
	dir := t.TempDir()

	// No stale files — should not panic.
	warnStaleUpgradeArtifacts(dir)

	// Add a stale file.
	if err := os.WriteFile(filepath.Join(dir, "stellad.bak"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Should not panic; we just verify it doesn't error.
	warnStaleUpgradeArtifacts(dir)

	// Nonexistent dir — should not panic.
	warnStaleUpgradeArtifacts(filepath.Join(dir, "nope"))
}
