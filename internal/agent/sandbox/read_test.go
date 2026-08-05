package sandbox

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

	"github.com/CherryHQ/stella/internal/fsops"
	"github.com/CherryHQ/stella/internal/vision"
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

type readTestSession struct {
	pkgsandbox.Session
	root string
}

func (s readTestSession) WorkingDir() string { return "/workspace" }
func (s readTestSession) Filesystem() (pkgsandbox.Filesystem, error) {
	return fsops.NewFilesystem([]fsops.Mount{{Path: pkgsandbox.PathWorkspace, Directory: s.root}})
}

func newTestReadTool(projectRoot string) *hostReadTool {
	return &hostReadTool{session: readTestSession{Session: pkgsandbox.NopSession(), root: projectRoot}}
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
	if cfg.Width > vision.MaxImageDim || cfg.Height > vision.MaxImageDim {
		t.Errorf("image not resized: %dx%d exceeds %d", cfg.Width, cfg.Height, vision.MaxImageDim)
	}
}

func TestReadImageCanonicalResultKeepsOriginalForTransform(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	writePNG(t, path, 10, 10)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := newTestReadTool(dir).ExecuteContent(pkgtools.WithImageResultMode(context.Background(), pkgtools.ImageResultCanonical), map[string]any{"path": "pic.png"})
	if err != nil || !ai.HasImage(blocks) {
		t.Fatalf("canonical read = %#v, %v", blocks, err)
	}
	for _, block := range blocks {
		if img, ok := block.(ai.ImageContent); ok {
			got, _ := base64.StdEncoding.DecodeString(img.Data)
			if !bytes.Equal(got, original) {
				t.Fatal("canonical read changed original bytes")
			}
		}
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

// unshadowedTempDir returns a temporary directory outside the system temp tree.
// A local sandbox session binds session-private directories over /tmp and
// /var/tmp, so a host path under them is ambiguous with sandbox-space temp paths
// and resolves into the private backing instead of the host file. Tests that
// hand host paths to a real session must stay clear of that overlap.
func unshadowedTempDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	dir, err := os.MkdirTemp(home, "stella-sandbox-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp(%q): %v", home, err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) }) //nolint:errcheck
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return resolved
}

func TestReadToolRejectsHostAbsolutePath(t *testing.T) {
	workspace := unshadowedTempDir(t)
	inside := filepath.Join(workspace, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside workspace\n"), 0o644); err != nil {
		t.Fatalf("write inside: %v", err)
	}
	outside := filepath.Join(unshadowedTempDir(t), "secret.txt")
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

	fsSession, ok := session.(pkgsandbox.FilesystemSession)
	if !ok {
		t.Fatal("local session lacks filesystem capability")
	}
	tool := newReadTool(fsSession)
	out, err := tool.Execute(context.Background(), map[string]any{"path": "/workspace/inside.txt"})
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
