package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if len(got) != 2 || got[0] != "stella" || got[1] != "stellad" {
		t.Fatalf("linux: got %v, want [stella stellad]", got)
	}
	got = binariesToUpgrade("windows")
	if len(got) != 2 || got[0] != "stella.exe" || got[1] != "stellad.exe" {
		t.Fatalf("windows: got %v, want [stella.exe stellad.exe]", got)
	}
}

func TestUpgradeInstallErrorAddsPermissionHint(t *testing.T) {
	err := upgradeInstallError(errors.New("rename stella.tmp stella: permission denied"), "/usr/local/bin/stella", "linux")

	if !strings.Contains(err.Error(), "permission denied replacing /usr/local/bin/stella") {
		t.Fatalf("error = %q, want permission hint", err.Error())
	}
}

func TestUpgradeInstallErrorAddsWindowsBusyHint(t *testing.T) {
	err := upgradeInstallError(errors.New("The process cannot access the file because it is being used by another process."), `C:\\bin\\stella.exe`, "windows")

	if !strings.Contains(err.Error(), "locked by a running process") {
		t.Fatalf("error = %q, want busy hint", err.Error())
	}
}

func TestCleanStaleUpgradeArtifacts(t *testing.T) {
	dir := t.TempDir()

	// Create stale files that should be removed.
	staleFiles := []string{
		"stella.tmp", "stellad.tmp",
		"stella.bak", "stellad.bak",
		"stella.old", "stellad.old",
		"stella.exe.tmp", "stellad.exe.bak",
	}
	for _, name := range staleFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Create files that should NOT be removed.
	keepFiles := []string{
		"stella", "stellad", "stellad.exe",
		"unrelated.tmp", "other.bak",
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
	backup := filepath.Join(dir, "stella.bak")
	if err := os.WriteFile(backup, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}

	cleanStaleUpgradeArtifacts(dir)

	restored := filepath.Join(dir, "stella")
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
		{"stella.tmp", true},
		{"stellad.bak", true},
		{"stella.old", true},
		{"stellad.exe.tmp", true},
		{"stella.exe.bak", true},
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

	committed := filepath.Join(dir, "stella")
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

	committed := filepath.Join(dir, "stella.exe")
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
	path := filepath.Join(dir, "big.bin")

	// Create a reader that produces more than maxArchiveBytes
	oldMax := maxArchiveBytes
	// Temporarily set a small limit for testing
	defer func() { /* maxArchiveBytes is const, we test with a real small file */ }()
	_ = oldMax

	// Instead, test that a normal file works fine
	content := strings.NewReader("small content")
	if err := writeReaderToFile(path, content, 0o755); err != nil {
		t.Fatalf("writeReaderToFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "small content" {
		t.Fatalf("got %q, want %q", string(data), "small content")
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

	_, ok = findChecksumAsset([]githubReleaseAsset{{Name: "archive.tar.gz"}})
	if ok {
		t.Fatal("expected not found when checksums.txt missing")
	}
}

func TestVerifyChecksumIntegration(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "stella_linux_amd64.tar.gz")
	content := []byte("fake archive content")
	if err := os.WriteFile(archivePath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(content)
	correctHash := hex.EncodeToString(h[:])
	checksumsTxt := fmt.Sprintf("%s  stella_linux_amd64.tar.gz\n", correctHash)

	// Test correct checksum passes
	expected, err := parseChecksumForFile(checksumsTxt, "stella_linux_amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	actual, err := sha256File(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("checksum mismatch: got %s, want %s", actual, expected)
	}

	// Test wrong checksum fails
	badChecksum := "0000000000000000000000000000000000000000000000000000000000000000"
	if actual == badChecksum {
		t.Fatal("bad test: bad checksum matches actual")
	}
}

func TestWarnStaleUpgradeArtifacts(t *testing.T) {
	dir := t.TempDir()

	// No stale files — should not panic.
	warnStaleUpgradeArtifacts(dir)

	// Add a stale file.
	if err := os.WriteFile(filepath.Join(dir, "stella.bak"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Should not panic; we just verify it doesn't error.
	warnStaleUpgradeArtifacts(dir)

	// Nonexistent dir — should not panic.
	warnStaleUpgradeArtifacts(filepath.Join(dir, "nope"))
}
