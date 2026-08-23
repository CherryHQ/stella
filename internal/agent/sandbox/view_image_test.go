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

	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

func writeViewImagePNG(t *testing.T, path string, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.SetRGBA(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return buf.Bytes()
}

func viewImageBlock(t *testing.T, blocks []ai.ContentBlock) ai.ImageContent {
	t.Helper()
	for _, block := range blocks {
		if imageBlock, ok := block.(ai.ImageContent); ok {
			return imageBlock
		}
	}
	t.Fatalf("content has no image block: %#v", blocks)
	return ai.ImageContent{}
}

func TestViewImageDefinitionHasOnlyRequiredPath(t *testing.T) {
	definition := viewImageDefinition()
	if definition.Name != "view_image" {
		t.Fatalf("name = %q, want view_image", definition.Name)
	}
	properties := definition.InputSchema["properties"].(map[string]any)
	if len(properties) != 1 || properties["path"] == nil {
		t.Fatalf("properties = %#v, want only path", properties)
	}
	required := definition.InputSchema["required"].([]string)
	if len(required) != 1 || required[0] != "path" {
		t.Fatalf("required = %#v, want path", required)
	}
	for _, want := range []string{"bash", "characters", "pixels", "$HOME", "$STELLA_ASSETS_DIR", "$TMPDIR"} {
		if !strings.Contains(definition.Description, want) {
			t.Errorf("description = %q, missing %q", definition.Description, want)
		}
	}
}

func TestViewImageIsUnconditionallyReservedAndAvailable(t *testing.T) {
	definitions := ReservedToolDefinitions()
	availability := ToolDefinitionsWithAvailability(false)
	if len(definitions) != 3 || len(availability) != 3 {
		t.Fatalf("definitions/availability lengths = %d/%d, want 3/3", len(definitions), len(availability))
	}
	for i, want := range []string{"bash", "view_image", "vllm"} {
		if definitions[i].Name != want || availability[i].Definition.Name != want {
			t.Fatalf("tool[%d] = %q/%q, want %q", i, definitions[i].Name, availability[i].Definition.Name, want)
		}
	}
	if !availability[0].Available || !availability[1].Available || availability[2].Available {
		t.Fatalf("availability = %#v, want bash/view_image true and vllm false", availability)
	}
}

func TestViewImageCanonicalResultKeepsOriginalBytes(t *testing.T) {
	dir := t.TempDir()
	original := writeViewImagePNG(t, filepath.Join(dir, "image.png"), 10, 10)
	ctx := pkgtools.WithImageResultMode(context.Background(), pkgtools.ImageResultCanonical)
	blocks, err := pkgtools.ExecuteToolContent(ctx, newViewImageTool(newTestVisionSession(t, dir)), map[string]any{"path": "image.png"})
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	imageBlock := viewImageBlock(t, blocks)
	got, err := base64.StdEncoding.DecodeString(imageBlock.Data)
	if err != nil {
		t.Fatalf("decode image: %v", err)
	}
	if imageBlock.MimeType != "image/png" || !bytes.Equal(got, original) {
		t.Fatalf("canonical image = mime:%q bytes_equal:%t", imageBlock.MimeType, bytes.Equal(got, original))
	}
}

func TestViewImageLegacyResultUsesInlinePreparation(t *testing.T) {
	dir := t.TempDir()
	writeViewImagePNG(t, filepath.Join(dir, "large.png"), 3000, 100)
	blocks, err := pkgtools.ExecuteToolContent(context.Background(), newViewImageTool(newTestVisionSession(t, dir)), map[string]any{"path": "large.png"})
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(viewImageBlock(t, blocks).Data)
	if err != nil {
		t.Fatalf("decode image: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.Width > vision.MaxImageDim || cfg.Height > vision.MaxImageDim {
		t.Fatalf("inline image = %dx%d, exceeds %d", cfg.Width, cfg.Height, vision.MaxImageDim)
	}
}

func TestViewImageExecuteReturnsUnderstandableText(t *testing.T) {
	dir := t.TempDir()
	writeViewImagePNG(t, filepath.Join(dir, "image.png"), 2, 2)
	got, err := newViewImageTool(newTestVisionSession(t, dir)).Execute(context.Background(), map[string]any{"path": "image.png"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "Viewed image file [image/png]" {
		t.Fatalf("Execute = %q", got)
	}
}

func TestViewImageRejectsNonImageWithActionableError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain text"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := pkgtools.ExecuteToolContent(context.Background(), newViewImageTool(newTestVisionSession(t, dir)), map[string]any{"path": "notes.txt"})
	if err == nil {
		t.Fatal("expected non-image error")
	}
	for _, want := range []string{"not a recognized image", "bash", "xberg extract"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, missing %q", err, want)
		}
	}
}

func TestViewImageRejectsRecognizedButInvalidImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.png"), []byte("\x89PNG\r\n\x1a\nbroken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := pkgtools.ExecuteToolContent(context.Background(), newViewImageTool(newTestVisionSession(t, dir)), map[string]any{"path": "broken.png"})
	if err == nil || !strings.Contains(err.Error(), "image failed safety validation") {
		t.Fatalf("error = %v, want safety validation failure", err)
	}
}

type viewImageTestHost struct {
	pkgsandbox.Session
	policy     pkgsandbox.Policy
	workingDir string
	files      pkgsandbox.FileAccess
}

func (h *viewImageTestHost) Policy() pkgsandbox.Policy    { return h.policy }
func (h *viewImageTestHost) WorkingDir() string           { return h.workingDir }
func (h *viewImageTestHost) Files() pkgsandbox.FileAccess { return h.files }

type viewImageTestFiles struct {
	pkgsandbox.FileAccess
	path string
	data []byte
}

func (f viewImageTestFiles) ReadFile(path string) ([]byte, error) {
	if path != f.path {
		return nil, os.ErrPermission
	}
	return f.data, nil
}

func TestViewImageUsesSelectedFileViewRoots(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "image.png")
	data := writeViewImagePNG(t, imagePath, 2, 2)
	host := &viewImageTestHost{
		policy:     pkgsandbox.Policy{Env: map[string]string{pkgsandbox.EnvStellaAssetsDir: "/user/assets"}},
		workingDir: "/workspace",
		files:      viewImageTestFiles{path: "/user/assets/image.png", data: data},
	}
	blocks, err := pkgtools.ExecuteToolContent(context.Background(), newViewImageTool(host), map[string]any{"path": "$STELLA_ASSETS_DIR/image.png"})
	if err != nil || !ai.HasImage(blocks) {
		t.Fatalf("ExecuteContent = %#v, %v", blocks, err)
	}
}
