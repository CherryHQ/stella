package mlruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMissing(t *testing.T) {
	// Empty STELLA_HOME, no dev overrides => nothing installed, no error.
	t.Setenv(EnvRuntimeDir, "")
	t.Setenv(EnvModelDir, "")
	_, found, err := Resolve(t.TempDir())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if found {
		t.Fatal("expected not found for an empty STELLA_HOME")
	}
}

func TestResolveDevOverride(t *testing.T) {
	if !Supported() {
		t.Skip("unsupported platform")
	}
	rt := t.TempDir()
	md := t.TempDir()
	// Lay down a binary + lib dir + models so Resolve treats it as installed.
	mustWrite(t, filepath.Join(rt, binFile), "#!/bin/true\n")
	if err := os.MkdirAll(filepath.Join(rt, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(md, embedModelFile), "x")
	mustWrite(t, filepath.Join(md, tokenizerFile), "{}")

	t.Setenv(EnvRuntimeDir, rt)
	t.Setenv(EnvModelDir, md)

	got, found, err := Resolve("/unused")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !found {
		t.Fatal("expected found with dev overrides")
	}
	if got.BinPath != filepath.Join(rt, binFile) {
		t.Errorf("bin path = %q", got.BinPath)
	}
	if got.LibDir != filepath.Join(rt, "lib") {
		t.Errorf("lib dir = %q, want the lib/ subdir", got.LibDir)
	}
	if got.EmbedModelPath != filepath.Join(md, embedModelFile) {
		t.Errorf("model path = %q", got.EmbedModelPath)
	}
}

func TestResolveOCROptional(t *testing.T) {
	if !Supported() {
		t.Skip("unsupported platform")
	}
	rt := t.TempDir()
	md := t.TempDir()
	mustWrite(t, filepath.Join(rt, binFile), "#!/bin/true\n")
	if err := os.MkdirAll(filepath.Join(rt, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(md, embedModelFile), "x")
	mustWrite(t, filepath.Join(md, tokenizerFile), "{}")
	t.Setenv(EnvRuntimeDir, rt)
	t.Setenv(EnvModelDir, md)

	// Without OCR assets, embedding still resolves and HasOCR is false.
	got, found, err := Resolve("/unused")
	if err != nil || !found {
		t.Fatalf("resolve embed-only: found=%v err=%v", found, err)
	}
	if got.HasOCR() {
		t.Fatal("HasOCR true with no OCR models present")
	}

	// A partial OCR install (det only) must not half-enable OCR.
	mustWrite(t, filepath.Join(md, ocrDetFile), "d")
	got, _, _ = Resolve("/unused")
	if got.HasOCR() {
		t.Fatal("HasOCR true with a partial OCR install")
	}

	// Full det+rec+keys set enables OCR.
	mustWrite(t, filepath.Join(md, ocrRecFile), "r")
	mustWrite(t, filepath.Join(md, ocrKeysFile), "k")
	got, _, _ = Resolve("/unused")
	if !got.HasOCR() {
		t.Fatal("HasOCR false with a full OCR install")
	}
	if got.OCRDetPath != filepath.Join(md, ocrDetFile) {
		t.Errorf("det path = %q", got.OCRDetPath)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
