package sandbox

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

func readDefinition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "read",
		Description: "Read the contents of a file. Supports text files and images (jpg, png, gif, webp); safe images are returned as attachments and the agent runtime decides whether the model receives pixels. Text output is truncated to 2000 lines or 50KB. Use offset and limit to paginate through large files.",
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

func newReadTool(host pkgsandbox.Session) pkgtools.Tool {
	return &hostReadTool{host: host}
}

type hostReadTool struct {
	host pkgsandbox.Session
}

const (
	// Wide CSV rows can easily reach a few hundred bytes. This conservative
	// default aims below the per-call output budget; #1118's truncation marker
	// remains the fallback when actual rows are wider.
	conservativeReadSliceBytesPerLine = 250
	bytesPerKiB                       = 1024
)

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

	view, err := pkgsandbox.SelectFileView(ctx, t.host)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	resolvedPath, err := resolveToolExpression(view.Policy.Env, view.WorkingDir, path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	content, err := view.Files.ReadFile(resolvedPath)
	if err != nil {
		var tooLarge *pkgsandbox.FileTooLargeError
		if errors.As(err, &tooLarge) {
			outputByteLimit := pkgtools.OutputByteLimit()
			sliceLines := max(outputByteLimit/conservativeReadSliceBytesPerLine, 1)
			outputLimitKB := max((outputByteLimit+bytesPerKiB-1)/bytesPerKiB, 1)
			command := fmt.Sprintf("sed -n '1,%dp' -- %s", sliceLines, shellQuote(path))
			return nil, fmt.Errorf("read %q: file is %d bytes, over the %d-byte read cap. Tool output is capped at ~%d KB per call, so start with a %d-line slice from the beginning, not the whole file. Next call: bash(command=%q)", path, tooLarge.Size, tooLarge.Limit, outputLimitKB, sliceLines, command)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Image/binary handling applies to the first page only.
	if offset <= 1 {
		if mime := pkgtools.DetectImageMime(content); mime != "" {
			return t.imageBlocks(ctx, path, content, mime), nil
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
// sessions return the original safe bytes for immutable persistence; deferred
// groups resize before their legacy inline write. Model capability is handled
// later at the provider projection boundary.
func (t *hostReadTool) imageBlocks(ctx context.Context, displayPath string, content []byte, mime string) []ai.ContentBlock {
	cfg, err := vision.ValidateBudget(content)
	if err != nil {
		return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s, but it exceeds the safe decode budget: %v", mime, displayPath, err)}}
	}

	if pkgtools.ImageResultModeFromContext(ctx) == pkgtools.ImageResultCanonical {
		return []ai.ContentBlock{
			ai.TextContent{Text: fmt.Sprintf("Read image file [%s]", mime)},
			ai.ImageContent{Data: base64.StdEncoding.EncodeToString(content), MimeType: mime},
		}
	}

	data, outMime, err := vision.PrepareInline(content, cfg, mime)
	if err != nil {
		return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s, but it could not be processed for inlining: %v", mime, displayPath, err)}}
	}
	if len(data) > vision.MaxRendererPayloadBytes {
		return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s, but it is too large to inline (%d bytes).", outMime, displayPath, len(data))}}
	}
	return []ai.ContentBlock{
		ai.TextContent{Text: fmt.Sprintf("Read image file [%s]", outMime)},
		ai.ImageContent{Data: base64.StdEncoding.EncodeToString(data), MimeType: outMime},
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
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
