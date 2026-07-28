package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

func TestInspectCandidateArchiveUsesBinaryHeader(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binaryName := "stellad"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	archivePath := filepath.Join(root, "dist", "candidate.tar.gz")
	writeTestArchive(t, archivePath, map[string]string{
		"LICENSE":      "license",
		"README.md":    "readme",
		"README.zh.md": "readme zh",
	}, executable, binaryName)

	candidate := releasecontract.CandidateFile{
		Kind: "archive",
		Name: filepath.Base(archivePath),
		Path: filepath.ToSlash(filepath.Join("dist", filepath.Base(archivePath))),
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}
	output := filepath.Join(root, "candidate-bin", binaryName)
	inspection, err := inspectCandidateArchive(root, candidate, output)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Fatalf("platform = %q", inspection.Platform)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("extracted candidate mode = %s, want executable", info.Mode())
	}
}

func TestInspectCandidateArchiveRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "dist", "candidate.tar.gz")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "../stellad", Mode: 0o755, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = inspectCandidateArchive(root, releasecontract.CandidateFile{
		Kind: "archive",
		Name: filepath.Base(archivePath),
		Path: filepath.ToSlash(filepath.Join("dist", filepath.Base(archivePath))),
		OS:   "linux",
		Arch: "amd64",
	}, "")
	if err == nil {
		t.Fatal("unsafe archive path was accepted")
	}
}

func TestCandidateArchiveMatrixFixture(t *testing.T) {
	root := os.Getenv("STELLA_RELEASE_FIXTURE_ROOT")
	manifestPath := os.Getenv("STELLA_RELEASE_FIXTURE_MANIFEST")
	if root == "" || manifestPath == "" {
		t.Skip("set STELLA_RELEASE_FIXTURE_ROOT and STELLA_RELEASE_FIXTURE_MANIFEST to inspect real candidates")
	}
	manifest, err := releasecontract.LoadCandidateManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := releasecontract.VerifyCandidateManifest(root, manifest, manifest.Run); err != nil {
		t.Fatal(err)
	}
	report, err := verifyArchiveMatrix(root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Archives) != 6 {
		t.Fatalf("archive count = %d, want 6", len(report.Archives))
	}
}

func writeTestArchive(
	t *testing.T,
	archivePath string,
	textFiles map[string]string,
	executable string,
	binaryName string,
) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range textFiles {
		if err := tarWriter.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tarWriter, content); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: binaryName, Mode: 0o755, Size: info.Size(), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	executableFile, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(tarWriter, executableFile); err != nil {
		t.Fatal(err)
	}
	if err := executableFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
