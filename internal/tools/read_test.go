package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
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
	return &hostReadTool{projectRoot: projectRoot}
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

func TestPrepareInlineImageRejectsPixelBomb(t *testing.T) {
	// A header claiming a huge canvas must be rejected before full decode.
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8000, 8000))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, _, err := prepareInlineImage(buf.Bytes(), "image/png"); err == nil {
		t.Fatal("expected oversized image (64MP) to be rejected before decode")
	}
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
