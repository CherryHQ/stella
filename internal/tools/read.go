package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp" // register webp decoder for imaging.Decode

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

const (
	// maxImageDim is the longest edge (px) an inlined image is resized to fit.
	maxImageDim = 2000
	// maxInlineImageBytes caps the encoded image size sent to the model,
	// staying under provider inline-image limits (Anthropic allows ~5MB).
	maxInlineImageBytes = 5 * 1024 * 1024
	// maxImageInputBytes caps the raw file size we are willing to decode,
	// rejecting oversized inputs before allocating any pixel buffer.
	maxImageInputBytes = 30 * 1024 * 1024
	// maxImagePixels bounds total pixels (width*height) decoded, guarding
	// against decompression bombs whose header is tiny but expand enormously.
	maxImagePixels = 50_000_000
	// kreuzbergTimeout bounds synchronous text extraction for non-vision models.
	kreuzbergTimeout = 60 * time.Second
)

func readDefinition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "read",
		Description: "Read the contents of a file. Supports text files and images (jpg, png, gif, webp); images are returned as attachments to vision models, or extracted to text otherwise. Text output is truncated to 2000 lines or 50KB. Use offset and limit to paginate through large files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Absolute or relative path to the file to read."},
				"offset": map[string]any{"type": "integer", "description": "Line number to start reading from (1-based). Defaults to 1."},
				"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read. Defaults to all lines."},
			},
			"required": []string{"path"},
		},
	}
}

func newReadTool(host sandbox.Host, projectRoot string) pkgtools.Tool {
	return &hostReadTool{host: host, projectRoot: projectRoot}
}

type hostReadTool struct {
	host        sandbox.Host
	projectRoot string
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
		if mime := detectImageMime(content); mime != "" {
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

// imageBlocks turns an image file into content blocks. Vision-capable models get
// the (resized) image inline; otherwise the image is extracted to text via
// kreuzberg, with a note explaining the substitution. Failures degrade to a
// text note rather than erroring, so a readable image never aborts the read.
func (t *hostReadTool) imageBlocks(ctx context.Context, displayPath, resolvedPath string, content []byte, mime string) []ai.ContentBlock {
	if pkgtools.VisionFromContext(ctx) {
		data, outMime, err := prepareInlineImage(content, mime)
		if err != nil {
			return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s, but it could not be processed for inlining: %v", mime, displayPath, err)}}
		}
		if len(data) > maxInlineImageBytes {
			return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s, but it is too large to inline (%d bytes).", outMime, displayPath, len(data))}}
		}
		return []ai.ContentBlock{
			ai.TextContent{Text: fmt.Sprintf("Read image file [%s]", outMime)},
			ai.ImageContent{Data: base64.StdEncoding.EncodeToString(data), MimeType: outMime},
		}
	}

	text, err := extractWithKreuzberg(ctx, resolvedPath)
	if err != nil {
		return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s. The current model cannot view images and text extraction failed: %v", mime, displayPath, err)}}
	}
	return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s. The current model cannot view images; extracted text via kreuzberg:\n\n%s", mime, displayPath, text)}}
}

// detectImageMime returns the canonical MIME type for supported image bytes, or
// "" when the data is not a supported image.
func detectImageMime(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	ct := http.DetectContentType(data)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ct
	}
	return ""
}

// prepareInlineImage downsizes an image to fit maxImageDim on its longest edge,
// re-encoding only when a resize is needed. Images already within bounds are
// returned untouched. WebP is re-encoded as PNG since imaging cannot encode it.
func prepareInlineImage(data []byte, mime string) ([]byte, string, error) {
	if len(data) > maxImageInputBytes {
		return nil, "", fmt.Errorf("image input too large: %d bytes exceeds %d", len(data), maxImageInputBytes)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return nil, "", fmt.Errorf("image too large to decode: %dx%d exceeds %d pixels", cfg.Width, cfg.Height, maxImagePixels)
	}
	if cfg.Width <= maxImageDim && cfg.Height <= maxImageDim && mime != "image/webp" {
		return data, mime, nil
	}

	img, err := imaging.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	fitted := imaging.Fit(img, maxImageDim, maxImageDim, imaging.Lanczos)

	format, outMime := imaging.PNG, "image/png"
	switch mime {
	case "image/jpeg":
		format, outMime = imaging.JPEG, "image/jpeg"
	case "image/gif":
		format, outMime = imaging.GIF, "image/gif"
	}

	var buf bytes.Buffer
	if err := imaging.Encode(&buf, fitted, format); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), outMime, nil
}

// extractWithKreuzberg shells out to the kreuzberg CLI to extract text from a
// file. It returns an error when the binary is missing or extraction fails.
func extractWithKreuzberg(ctx context.Context, path string) (string, error) {
	bin, err := exec.LookPath("kreuzberg")
	if err != nil {
		return "", fmt.Errorf("kreuzberg not available: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, kreuzbergTimeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "extract", path).Output()
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("kreuzberg returned no text")
	}
	return text, nil
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
