package sandbox

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

const viewImageDescription = "View an image file as actual pixels in the current model turn. Use it for jpg, png, gif, or webp when the parent model must see the image itself; use bash instead when you need file metadata or characters extracted with OCR or `xberg extract`. Bash returns characters, while this tool returns pixels directly to the parent model. It does not call a vision model. Supported roots: $HOME, $STELLA_ASSETS_DIR, and $TMPDIR."

func viewImageDefinition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "view_image",
		Description: viewImageDescription,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Path to the image file. Relative paths are working/project files. Supported roots: $HOME, $STELLA_ASSETS_DIR, and $TMPDIR."},
			},
			"required": []string{"path"},
		},
	}
}

func newViewImageTool(host pkgsandbox.Session) pkgtools.Tool {
	return &hostViewImageTool{host: host}
}

type hostViewImageTool struct {
	host pkgsandbox.Session
}

var (
	_ pkgtools.Tool        = (*hostViewImageTool)(nil)
	_ pkgtools.ContentTool = (*hostViewImageTool)(nil)
)

func (t *hostViewImageTool) Definition() pkgtools.Definition { return viewImageDefinition() }

func (t *hostViewImageTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	blocks, err := t.ExecuteContent(ctx, args)
	if err != nil {
		return "", err
	}
	return ai.FlattenText(blocks), nil
}

func (t *hostViewImageTool) ExecuteContent(ctx context.Context, args map[string]any) ([]ai.ContentBlock, error) {
	path := pkgtools.StringArg(args, "path")
	if path == "" {
		return nil, fmt.Errorf("view_image: path is required")
	}

	view, err := pkgsandbox.SelectFileView(ctx, t.host)
	if err != nil {
		return nil, fmt.Errorf("view_image %s: %w", path, err)
	}
	resolvedPath, err := resolveToolExpression(view.Policy.Env, view.WorkingDir, path)
	if err != nil {
		return nil, fmt.Errorf("view_image %s: %w", path, err)
	}
	content, err := view.Files.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("view_image %s: %w", path, err)
	}

	mime := pkgtools.DetectImageMime(content)
	if mime == "" {
		return nil, fmt.Errorf("view_image %s: not a recognized image — use bash with `xberg extract` for documents and text", path)
	}
	cfg, detectedMIME, err := vision.ValidateImage(content, mime)
	if err != nil {
		return nil, fmt.Errorf("view_image %s: image failed safety validation: %w", path, err)
	}

	data := content
	outMIME := detectedMIME
	if pkgtools.ImageResultModeFromContext(ctx) != pkgtools.ImageResultCanonical {
		data, outMIME, err = vision.PrepareInline(content, cfg, detectedMIME)
		if err != nil {
			return nil, fmt.Errorf("view_image %s: prepare inline image: %w", path, err)
		}
		if len(data) > vision.MaxRendererPayloadBytes {
			return nil, fmt.Errorf("view_image %s: image is too large to inline: %d bytes exceeds %d", path, len(data), vision.MaxRendererPayloadBytes)
		}
	}

	return []ai.ContentBlock{
		ai.TextContent{Text: fmt.Sprintf("Viewed image file [%s]", outMIME)},
		ai.ImageContent{Data: base64.StdEncoding.EncodeToString(data), MimeType: outMIME},
	}, nil
}
