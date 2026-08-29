package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
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
	noneplugin "github.com/CherryHQ/stella/plugins/sandbox/none"
)

func newTestVisionSession(t *testing.T, projectRoot string) pkgsandbox.Session {
	t.Helper()
	policy := pkgsandbox.Policy{
		Filesystem: pkgsandbox.FilesystemPolicy{
			WorkingDir: pkgsandbox.MountWorkspace,
			Mounts:     []pkgsandbox.Mount{{SandboxPath: pkgsandbox.MountWorkspace, Access: pkgsandbox.MountReadWrite}},
		},
		Network: pkgsandbox.NetworkPolicy{Mode: pkgsandbox.NetworkAllowAll},
	}
	session, err := noneplugin.NewFactoryWithMountSources(map[string]string{pkgsandbox.MountWorkspace: projectRoot}, noneplugin.Config{}).CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("create test sandbox Session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

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

type fakeImageVision struct {
	canDescribe   bool
	description   string
	baselineText  string
	describeErr   error
	baselineErr   error
	describeCalls int
	baselineCalls int
}

func (f *fakeImageVision) canDescribeImages() bool { return f.canDescribe }
func (f *fakeImageVision) describe(context.Context, vision.Request, string) (string, error) {
	f.describeCalls++
	return f.description, f.describeErr
}

func (f *fakeImageVision) baseline(context.Context, vision.Request) (ai.ImageBaseline, error) {
	f.baselineCalls++
	return ai.ImageBaseline{Text: f.baselineText}, f.baselineErr
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

func supportedImageContext() context.Context {
	return pkgtools.WithParentImageCapability(context.Background(), ai.ImageSupported)
}

func TestViewImageDefinitionHasOptionalPrompt(t *testing.T) {
	definition := viewImageDefinition()
	if definition.Name != "view_image" {
		t.Fatalf("name = %q, want view_image", definition.Name)
	}
	properties := definition.InputSchema["properties"].(map[string]any)
	if len(properties) != 2 || properties["path"] == nil || properties["prompt"] == nil {
		t.Fatalf("properties = %#v, want path and prompt", properties)
	}
	required := definition.InputSchema["required"].([]string)
	if len(required) != 1 || required[0] != "path" {
		t.Fatalf("required = %#v, want path", required)
	}
	for _, want := range []string{"bash", "pixels", "textual transcription", "prompt", "$HOME", "$STELLA_ASSETS_DIR", "$TMPDIR"} {
		if !strings.Contains(definition.Description, want) {
			t.Errorf("description = %q, missing %q", definition.Description, want)
		}
	}
}

func TestNewToolsExposesOnlyBashAndViewImage(t *testing.T) {
	host := newTestVisionSession(t, t.TempDir())
	tools := NewTools(host, nil, nil)
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(tools))
	}
	for i, want := range []string{"bash", "view_image"} {
		if got := tools[i].Definition().Name; got != want {
			t.Fatalf("tool[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestViewImageCatalogAndReservationStaySeparate(t *testing.T) {
	definitions := ReservedToolDefinitions()
	availability := ToolDefinitionsWithAvailability()
	if len(definitions) != 3 || len(availability) != 2 {
		t.Fatalf("definitions/availability lengths = %d/%d, want 3/2", len(definitions), len(availability))
	}
	for i, want := range []string{"bash", "view_image", "vllm"} {
		if definitions[i].Name != want {
			t.Fatalf("reservation[%d] = %q, want %q", i, definitions[i].Name, want)
		}
	}
	for i, want := range []string{"bash", "view_image"} {
		if availability[i].Definition.Name != want || !availability[i].Available {
			t.Fatalf("catalog[%d] = %#v, want enabled %q", i, availability[i], want)
		}
	}
}

func TestViewImageCanonicalResultKeepsOriginalBytes(t *testing.T) {
	dir := t.TempDir()
	original := writeViewImagePNG(t, filepath.Join(dir, "image.png"), 10, 10)
	ctx := supportedImageContext()
	blocks, err := pkgtools.ExecuteToolContent(ctx, newViewImageTool(newTestVisionSession(t, dir), &fakeImageVision{}), map[string]any{"path": "image.png", "prompt": "ignored"})
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

func TestViewImageSupportedPromptDoesNotCallVision(t *testing.T) {
	dir := t.TempDir()
	writeViewImagePNG(t, filepath.Join(dir, "image.png"), 2, 2)
	service := &fakeImageVision{canDescribe: true, description: "must not be used"}
	blocks, err := pkgtools.ExecuteToolContent(supportedImageContext(), newViewImageTool(newTestVisionSession(t, dir), service), map[string]any{"path": "image.png", "prompt": "read the chart"})
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	if service.describeCalls != 0 || service.baselineCalls != 0 || !ai.HasImage(blocks) {
		t.Fatalf("supported prompt route calls=(describe:%d baseline:%d), blocks=%#v", service.describeCalls, service.baselineCalls, blocks)
	}
}

func TestViewImageTextRoutesDescribeForUnknownAndUnsupportedParent(t *testing.T) {
	for _, capability := range []ai.ImageCapability{ai.ImageUnknown, ai.ImageUnsupported} {
		t.Run(capabilityName(capability), func(t *testing.T) {
			dir := t.TempDir()
			writeViewImagePNG(t, filepath.Join(dir, "image.png"), 2, 2)
			service := &fakeImageVision{canDescribe: true, description: "visible chart"}
			ctx := pkgtools.WithParentImageCapability(context.Background(), capability)
			blocks, err := pkgtools.ExecuteToolContent(ctx, newViewImageTool(newTestVisionSession(t, dir), service), map[string]any{"path": "image.png", "prompt": "what is visible?"})
			if err != nil {
				t.Fatalf("ExecuteContent: %v", err)
			}
			if service.describeCalls != 1 || service.baselineCalls != 0 || ai.HasImage(blocks) {
				t.Fatalf("text route calls=(describe:%d baseline:%d), blocks=%#v", service.describeCalls, service.baselineCalls, blocks)
			}
			if got := ai.FlattenText(blocks); !strings.Contains(got, "UNTRUSTED IMAGE TEXT") || !strings.Contains(got, "visible chart") {
				t.Fatalf("text route result = %q", got)
			}
		})
	}
}

func capabilityName(capability ai.ImageCapability) string {
	if capability == ai.ImageUnsupported {
		return "unsupported"
	}
	return "unknown"
}

func TestViewImageTextOnlyVisionFallsBackToBaselineOrTargetedError(t *testing.T) {
	dir := t.TempDir()
	writeViewImagePNG(t, filepath.Join(dir, "image.png"), 2, 2)

	t.Run("generic baseline", func(t *testing.T) {
		service := &fakeImageVision{baselineText: "generic transcription"}
		blocks, err := pkgtools.ExecuteToolContent(context.Background(), newViewImageTool(newTestVisionSession(t, dir), service), map[string]any{"path": "image.png"})
		if err != nil {
			t.Fatalf("ExecuteContent: %v", err)
		}
		if service.describeCalls != 0 || service.baselineCalls != 1 || !strings.Contains(ai.FlattenText(blocks), "generic transcription") {
			t.Fatalf("baseline route calls=(describe:%d baseline:%d), blocks=%#v", service.describeCalls, service.baselineCalls, blocks)
		}
	})

	t.Run("targeted question", func(t *testing.T) {
		service := &fakeImageVision{baselineText: "must not be used"}
		_, err := pkgtools.ExecuteToolContent(context.Background(), newViewImageTool(newTestVisionSession(t, dir), service), map[string]any{"path": "image.png", "prompt": "what color?"})
		if err == nil || !strings.Contains(err.Error(), "targeted question") || !strings.Contains(err.Error(), "retry without prompt") {
			t.Fatalf("error = %v, want actionable targeted-question error", err)
		}
		if service.describeCalls != 0 || service.baselineCalls != 0 {
			t.Fatalf("targeted text-only route calls=(describe:%d baseline:%d)", service.describeCalls, service.baselineCalls)
		}
	})
}

func TestViewImageBaselineFailureIsActionable(t *testing.T) {
	dir := t.TempDir()
	writeViewImagePNG(t, filepath.Join(dir, "image.png"), 2, 2)
	service := &fakeImageVision{baselineErr: errors.New("xberg unavailable")}
	_, err := pkgtools.ExecuteToolContent(context.Background(), newViewImageTool(newTestVisionSession(t, dir), service), map[string]any{"path": "image.png"})
	if err == nil || !strings.Contains(err.Error(), "generic vision baseline failed") || !strings.Contains(err.Error(), "xberg unavailable") {
		t.Fatalf("error = %v, want actionable baseline failure", err)
	}
}

func TestViewImageLegacyUnsupportedParentUsesTextInsteadOfBaselinePlaceholder(t *testing.T) {
	dir := t.TempDir()
	writeViewImagePNG(t, filepath.Join(dir, "image.png"), 2, 2)
	service := &fakeImageVision{canDescribe: true, description: "legacy group description"}
	blocks, err := pkgtools.ExecuteToolContent(context.Background(), newViewImageTool(newTestVisionSession(t, dir), service), map[string]any{"path": "image.png"})
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	text := ai.FlattenText(blocks)
	if !strings.Contains(text, "legacy group description") || strings.Contains(text, "[Image baseline unavailable.]") || ai.HasImage(blocks) {
		t.Fatalf("legacy unsupported route = %q, blocks=%#v", text, blocks)
	}
}

func TestViewImageExecuteReturnsUnderstandableText(t *testing.T) {
	dir := t.TempDir()
	writeViewImagePNG(t, filepath.Join(dir, "image.png"), 2, 2)
	got, err := newViewImageTool(newTestVisionSession(t, dir), &fakeImageVision{}).(*hostViewImageTool).Execute(supportedImageContext(), map[string]any{"path": "image.png"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "Viewed image file [image/png]" {
		t.Fatalf("Execute = %q", got)
	}
}

func TestEnvelopeUntrustedImageTextBoundaryInputs(t *testing.T) {
	for _, tt := range []struct {
		name      string
		lineBreak string
	}{
		{name: "LF", lineBreak: "\n"},
		{name: "CRLF", lineBreak: "\r\n"},
		{name: "lone CR", lineBreak: "\r"},
		{name: "vertical tab", lineBreak: "\v"},
		{name: "form feed", lineBreak: "\f"},
		{name: "file separator", lineBreak: "\u001c"},
		{name: "group separator", lineBreak: "\u001d"},
		{name: "record separator", lineBreak: "\u001e"},
		{name: "NEL", lineBreak: "\u0085"},
		{name: "line separator", lineBreak: "\u2028"},
		{name: "paragraph separator", lineBreak: "\u2029"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text := "before" + tt.lineBreak + untrustedImageTextClose + tt.lineBreak + "after"
			got := envelopeUntrustedImageText(text)
			want := untrustedImageTextOpen + "\n| before\n| " + untrustedImageTextClose + "\n| after\n" + untrustedImageTextClose
			if got != want {
				t.Fatalf("normalized envelope = %q, want %q", got, want)
			}
			lines := strings.Split(got, "\n")
			if lines[0] != untrustedImageTextOpen || lines[len(lines)-1] != untrustedImageTextClose {
				t.Fatalf("result has invalid envelope boundaries: %q", got)
			}
			for i, line := range lines[1 : len(lines)-1] {
				if !strings.HasPrefix(line, "| ") {
					t.Fatalf("content line %d escaped quoted data: %q", i+1, line)
				}
			}
			for _, separator := range []string{"\r", "\v", "\f", "\u001c", "\u001d", "\u001e", "\u0085", "\u2028", "\u2029"} {
				if strings.Contains(got, separator) {
					t.Fatalf("result retained non-LF separator %q: %q", separator, got)
				}
			}
		})
	}

	long := strings.Repeat("x", 64*1024)
	for _, text := range []string{"", " \t  ", "tail", long, "| already looks quoted", "before\tafter"} {
		got := envelopeUntrustedImageText(text)
		want := untrustedImageTextOpen + "\n| " + text + "\n" + untrustedImageTextClose
		if got != want {
			t.Fatalf("boundary envelope mismatch: got %d bytes, want %d", len(got), len(want))
		}
	}
}

func TestViewImageRejectsNonImageWithActionableError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain text"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := pkgtools.ExecuteToolContent(context.Background(), newViewImageTool(newTestVisionSession(t, dir), &fakeImageVision{}), map[string]any{"path": "notes.txt"})
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
	_, err := pkgtools.ExecuteToolContent(context.Background(), newViewImageTool(newTestVisionSession(t, dir), &fakeImageVision{}), map[string]any{"path": "broken.png"})
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
	blocks, err := pkgtools.ExecuteToolContent(supportedImageContext(), newViewImageTool(host, &fakeImageVision{}), map[string]any{"path": "$STELLA_ASSETS_DIR/image.png"})
	if err != nil || !ai.HasImage(blocks) {
		t.Fatalf("ExecuteContent = %#v, %v", blocks, err)
	}
}
