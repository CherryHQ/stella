package builddeps

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractTarGzRejectsSymlinkTraversal(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "dest")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "link")); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(root, "test.tar.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	payload := []byte("owned")
	if err := tw.WriteHeader(&tar.Header{Name: "link/pwned.txt", Mode: 0o644, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ExtractTarGz(archive, dest); err == nil {
		t.Fatal("expected symlink traversal error")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file should not exist, stat err = %v", err)
	}
}

func TestExtractZipRejectsSymlinkTraversal(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "dest")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "link")); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(root, "test.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("link/pwned.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("owned")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := ExtractZip(archive, dest); err == nil {
		t.Fatal("expected symlink traversal error")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file should not exist, stat err = %v", err)
	}
}
