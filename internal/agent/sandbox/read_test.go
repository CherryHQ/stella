package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
	localplugin "github.com/CherryHQ/stella/plugins/sandbox/local"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

func newTestReadTool(projectRoot string) *hostReadTool {
	return &hostReadTool{host: pkgsandbox.NopSession(), projectRoot: projectRoot}
}

func TestExtractWithXbergUsesManagedShim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	shim := filepath.Join(pkgsandbox.MiseShimsDir(stellaHome), "xberg")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatalf("create shim directory: %v", err)
	}
	if err := os.WriteFile(shim, []byte("#!/bin/sh\n[ \"$1\" = extract ] || exit 1\nprintf %s \"$PWD\"\n"), 0o755); err != nil {
		t.Fatalf("write Xberg shim: %v", err)
	}
	inputDir := t.TempDir()

	got, err := extractWithXberg(context.Background(), filepath.Join(inputDir, "document.pdf"))
	if err != nil {
		t.Fatalf("extractWithXberg() error: %v", err)
	}
	want, err := filepath.EvalSymlinks(inputDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got != want {
		t.Errorf("extractWithXberg() cwd = %q, want %q", got, want)
	}
}

func TestReadImageVisionReturnsImageBlock(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "pic.png"), 10, 10)
	tool := newTestReadTool(dir)

	blocks, err := tool.ExecuteContent(context.Background(), map[string]any{"path": "pic.png"})
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	if !ai.HasImage(blocks) {
		t.Fatalf("expected an image block, got %#v", blocks)
	}
	var img ai.ImageContent
	for _, b := range blocks {
		if ic, ok := b.(ai.ImageContent); ok {
			img = ic
		}
	}
	if img.MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", img.MimeType)
	}
	if _, err := base64.StdEncoding.DecodeString(img.Data); err != nil {
		t.Errorf("image data is not valid base64: %v", err)
	}
}

func TestReadImageResizesLargeImage(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "big.png"), 3000, 100)
	tool := newTestReadTool(dir)

	blocks, err := tool.ExecuteContent(context.Background(), map[string]any{"path": "big.png"})
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	var img ai.ImageContent
	for _, b := range blocks {
		if ic, ok := b.(ai.ImageContent); ok {
			img = ic
		}
	}
	raw, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.Width > maxImageDim || cfg.Height > maxImageDim {
		t.Errorf("image not resized: %dx%d exceeds %d", cfg.Width, cfg.Height, maxImageDim)
	}
}

func TestValidateImageBudgetRejectsPixelBomb(t *testing.T) {
	// A header claiming a huge canvas must be rejected from the IHDR alone,
	// without allocating the full pixel buffer the bomb would expand to.
	header := pngHeaderWithDims(8000, 8000) // 64MP > maxImagePixels
	if _, err := validateImageBudget(header); err == nil {
		t.Fatal("expected oversized image (64MP) to be rejected before decode")
	}
}

// pngHeaderWithDims builds a minimal PNG (signature + IHDR chunk only) declaring
// the given dimensions. image.DecodeConfig reads the size from IHDR without
// decoding pixels, so this exercises the header-based budget check cheaply.
func pngHeaderWithDims(w, h uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // color type: RGBA truecolor (DecodeConfig returns after IHDR)
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, 13)
	buf.Write(length)
	chunk := append([]byte("IHDR"), ihdr...)
	buf.Write(chunk)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(chunk))
	buf.Write(crc)
	return buf.Bytes()
}

func TestReadImageNonVisionFallsBackToText(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "pic.png"), 10, 10)
	tool := newTestReadTool(dir)

	ctx := pkgtools.WithVision(context.Background(), false)
	blocks, err := tool.ExecuteContent(ctx, map[string]any{"path": "pic.png"})
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	if ai.HasImage(blocks) {
		t.Fatal("non-vision model must not receive an image block")
	}
	if !strings.Contains(ai.FlattenText(blocks), "cannot view images") {
		t.Errorf("expected non-vision note, got %q", ai.FlattenText(blocks))
	}
}

func TestReadTextFileReturnsTextBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := newTestReadTool(dir)

	blocks, err := tool.ExecuteContent(context.Background(), map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	if ai.HasImage(blocks) {
		t.Fatal("text file must not produce an image block")
	}
	if got := ai.FlattenText(blocks); !strings.Contains(got, "line1") || !strings.Contains(got, "line2") {
		t.Errorf("unexpected text output: %q", got)
	}
}

func TestReadToolProjectRootAbsolutePathUsesHostBoundary(t *testing.T) {
	rawWorkspace := t.TempDir()
	workspace, err := filepath.EvalSymlinks(rawWorkspace)
	if err != nil {
		t.Fatalf("EvalSymlinks workspace: %v", err)
	}
	inside := filepath.Join(workspace, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside workspace\n"), 0o644); err != nil {
		t.Fatalf("write inside: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside workspace\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	policy := pkgsandbox.Policy{
		Filesystem: pkgsandbox.FilesystemPolicy{
			WorkspaceRoot: workspace,
			WorkingDir:    workspace,
		},
		Network: pkgsandbox.NetworkPolicy{Mode: pkgsandbox.NetworkAllowAll},
	}
	session, err := localplugin.NewFactory().CreateSession(context.Background(), policy)
	if err != nil {
		t.Skipf("local sandbox unavailable: %v", err)
	}
	defer session.Close() //nolint:errcheck

	tool := newReadTool(session, workspace)
	out, err := tool.Execute(context.Background(), map[string]any{"path": inside})
	if err != nil {
		t.Fatalf("read inside workspace: %v", err)
	}
	if !strings.Contains(out, "inside workspace") {
		t.Fatalf("read inside output = %q", out)
	}

	out, err = tool.Execute(context.Background(), map[string]any{"path": outside})
	if err == nil {
		t.Fatalf("expected outside absolute path to be rejected, got output %q", out)
	}
}

func TestDetectImageMime(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if got := detectImageMime(buf.Bytes()); got != "image/png" {
		t.Errorf("png mime = %q, want image/png", got)
	}
	if got := detectImageMime([]byte("just text, not an image")); got != "" {
		t.Errorf("text mime = %q, want empty", got)
	}
}
