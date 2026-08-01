package sandbox

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

func readDefinition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "read",
		Description: "Read the contents of a file. Supports text files and images (jpg, png, gif, webp); images are returned as attachments to vision models, or extracted to text otherwise. Text output is truncated to 2000 lines or 50KB. Use offset and limit to paginate through large files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Relative paths are working/project files. Supported roots: $HOME, $STELLA_ASSETS_DIR, and $TMPDIR. Default work to $HOME; save final user deliverables in $STELLA_ASSETS_DIR when available."},
				"offset": map[string]any{"type": "integer", "description": "Line number to start reading from (1-based). Defaults to 1."},
				"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read. Defaults to all lines."},
			},
			"required": []string{"path"},
		},
	}
}

// newReadTool builds the read tool. A nil vision service is allowed and keeps
// the plain Xberg text fallback for non-vision models.
func newReadTool(host pkgsandbox.Host, projectRoot string, visionSvc *vision.Service) pkgtools.Tool {
	return &hostReadTool{host: host, projectRoot: projectRoot, vision: visionSvc}
}

type hostReadTool struct {
	host        pkgsandbox.Host
	projectRoot string
	vision      *vision.Service
}

func (t *hostReadTool) Definition() pkgtools.Definition { return readDefinition() }

func (t *hostReadTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	blocks, err := t.ExecuteContent(ctx, args)
	if err != nil {
		return "", err
	}
	return ai.FlattenText(blocks), nil
}

func (t *hostReadTool) ExecuteContent(ctx context.Context, args map[string]any) ([]ai.ContentBlock, error) {
	path := pkgtools.StringArg(args, "path")
	if path == "" {
		return nil, fmt.Errorf("read: path is required")
	}

	offset := max(toolIntArg(args, "offset", 1), 1)
	limit := toolIntArg(args, "limit", 0)

	resolvedPath, err := resolveToolPath(t.host, t.projectRoot, path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Image/binary handling applies to the first page only.
	if offset <= 1 {
		if mime := pkgtools.DetectImageMime(content); mime != "" {
			return t.imageBlocks(ctx, path, resolvedPath, content, mime), nil
		}
		sample := content
		if len(sample) > 8*1024 {
			sample = sample[:8*1024]
		}
		if len(sample) > 0 && pkgtools.IsBinary(string(sample)) {
			return nil, fmt.Errorf("read %s: binary file detected — use bash with xxd, file, or other tools to inspect binary content", path)
		}
	}

	paged, totalLines := paginateReadContent(string(content), offset, limit)
	if totalLines == 0 {
		return []ai.ContentBlock{ai.TextContent{Text: ""}}, nil
	}

	tr := pkgtools.TruncateHead(paged)
	linesConsumed := max(tr.OutputLines, 1)
	selectedLines := totalLines - (offset - 1)
	if limit > 0 {
		selectedLines = min(selectedLines, limit)
	}
	lastLineShown := offset + min(linesConsumed, selectedLines) - 1
	if lastLineShown < totalLines {
		tr.Content += fmt.Sprintf("\n[Use offset=%d to continue reading]", lastLineShown+1)
	}
	return []ai.ContentBlock{ai.TextContent{Text: tr.Content}}, nil
}

// imageBlocks turns an image file into content blocks. Ordinary canonical
// sessions return the original safe bytes for immutable persistence; provider
// hydration adapts them later. Deferred legacy sessions resize inline, while
// text-only legacy paths render through the vision service or Xberg. Failures
// degrade to a text note rather than aborting the read.
func (t *hostReadTool) imageBlocks(ctx context.Context, displayPath, resolvedPath string, content []byte, mime string) []ai.ContentBlock {
	cfg, err := vision.ValidateBudget(content)
	if err != nil {
		return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s, but it exceeds the safe decode budget: %v", mime, displayPath, err)}}
	}

	if pkgtools.CanonicalImagesFromContext(ctx) {
		return []ai.ContentBlock{
			ai.TextContent{Text: fmt.Sprintf("Read image file [%s]", mime)},
			ai.ImageContent{Data: base64.StdEncoding.EncodeToString(content), MimeType: mime},
		}
	}

	if pkgtools.VisionFromContext(ctx) {
		data, outMime, err := vision.PrepareInline(content, cfg, mime)
		if err != nil {
			return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s, but it could not be processed for inlining: %v", mime, displayPath, err)}}
		}
		if len(data) > vision.MaxInlineBytes {
			return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s, but it is too large to inline (%d bytes).", outMime, displayPath, len(data))}}
		}
		return []ai.ContentBlock{
			ai.TextContent{Text: fmt.Sprintf("Read image file [%s]", outMime)},
			ai.ImageContent{Data: base64.StdEncoding.EncodeToString(data), MimeType: outMime},
		}
	}

	// The bytes already sit on disk, so hand the service the path: the Xberg
	// fallback then reads the original file instead of staging a copy.
	// "not configured to receive images" rather than "cannot see": this path is
	// also taken for a model that never declared its modalities, and telling a
	// model a falsehood about itself invites it to argue with the premise.
	res, err := t.vision.Understand(ctx, vision.Request{Data: content, MimeType: mime, Path: resolvedPath})
	if err != nil {
		return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s. This model is not configured to receive images and text extraction failed: %v", mime, displayPath, err)}}
	}
	return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s. This model is not configured to receive images; rendered as text via %s:\n\n%s", mime, displayPath, res.Source, res.Text)}}
}

func paginateReadContent(content string, offset, limit int) (string, int) {
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	totalLines := len(lines)
	if totalLines == 0 || offset > totalLines {
		return "", totalLines
	}
	selected := lines[offset-1:]
	if limit > 0 && len(selected) > limit {
		selected = selected[:limit]
	}
	return strings.Join(selected, ""), totalLines
}
