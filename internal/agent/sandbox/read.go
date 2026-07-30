package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register webp decoder for image.Decode

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
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
	// xbergTimeout bounds synchronous text extraction for non-vision models.
	xbergTimeout = 60 * time.Second
)

func readDefinition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "read",
		Description: "Read the contents of a file. Supports text files and images (jpg, png, gif, webp); images are returned as attachments to vision models, or extracted to text otherwise. Text output is truncated to 2000 lines or 50KB. Use offset and limit to paginate through large files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Relative paths are working/project files. Supported roots: $HOME, $STELLA_ASSETS_DIR, and $TMPDIR ($STELLA_USER_DIR is supported for compatibility). Default work to $HOME; save final user deliverables in $STELLA_ASSETS_DIR when available."},
				"offset": map[string]any{"type": "integer", "description": "Line number to start reading from (1-based). Defaults to 1."},
				"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read. Defaults to all lines."},
			},
			"required": []string{"path"},
		},
	}
}

func newReadTool(host pkgsandbox.Host, projectRoot string) pkgtools.Tool {
	return &hostReadTool{host: host, projectRoot: projectRoot}
}

type hostReadTool struct {
	host        pkgsandbox.Host
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

// imageBlocks turns an image file into content blocks. Vision-capable models get
// the (resized) image inline; otherwise the image is extracted to text via
// Xberg, with a note explaining the substitution. Failures degrade to a
// text note rather than erroring, so a readable image never aborts the read.
func (t *hostReadTool) imageBlocks(ctx context.Context, displayPath, resolvedPath string, content []byte, mime string) []ai.ContentBlock {
	cfg, err := validateImageBudget(content)
	if err != nil {
		return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s, but it exceeds the safe decode budget: %v", mime, displayPath, err)}}
	}

	if pkgtools.VisionFromContext(ctx) {
		data, outMime, err := prepareInlineImage(content, cfg, mime)
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

	text, err := extractWithXberg(ctx, resolvedPath)
	if err != nil {
		return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s. The current model cannot view images and text extraction failed: %v", mime, displayPath, err)}}
	}
	return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Read image file [%s] at %s. The current model cannot view images; extracted text via Xberg:\n\n%s", mime, displayPath, text)}}
}

// validateImageBudget rejects oversized inputs before any full decode allocates a
// pixel buffer: first by raw byte size, then by the decoded dimensions read from
// the header alone. It returns the parsed config so callers can reuse it without
// decoding the header twice. Runs on every image path (vision inline and the
// Xberg text fallback) so a decompression bomb cannot reach either decoder.
func validateImageBudget(data []byte) (image.Config, error) {
	if len(data) > maxImageInputBytes {
		return image.Config{}, fmt.Errorf("image input too large: %d bytes exceeds %d", len(data), maxImageInputBytes)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Config{}, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return image.Config{}, fmt.Errorf("image too large to decode: %dx%d exceeds %d pixels", cfg.Width, cfg.Height, maxImagePixels)
	}
	return cfg, nil
}

// prepareInlineImage downsizes an image to fit maxImageDim on its longest edge,
// re-encoding only when a resize is needed. Images already within bounds are
// returned untouched. WebP is re-encoded as PNG since the standard library
// cannot encode it. The caller must pass the config from a prior
// validateImageBudget check.
func prepareInlineImage(data []byte, cfg image.Config, mime string) ([]byte, string, error) {
	if cfg.Width <= maxImageDim && cfg.Height <= maxImageDim && mime != "image/webp" {
		return data, mime, nil
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	fitted := fitImage(img, maxImageDim)

	var buf bytes.Buffer
	outMime := "image/png"
	switch mime {
	case "image/jpeg":
		outMime = "image/jpeg"
		err = jpeg.Encode(&buf, fitted, &jpeg.Options{Quality: 90})
	case "image/gif":
		outMime = "image/gif"
		err = gif.Encode(&buf, fitted, nil)
	default:
		err = png.Encode(&buf, fitted)
	}
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), outMime, nil
}

// fitImage scales src down so its longest edge is at most maxDim, preserving
// aspect ratio. Images already within bounds are returned unchanged; src is
// never upscaled.
func fitImage(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return src
	}
	scale := math.Min(float64(maxDim)/float64(w), float64(maxDim)/float64(h))
	dstW := max(int(math.Round(float64(w)*scale)), 1)
	dstH := max(int(math.Round(float64(h)*scale)), 1)
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// extractWithXberg shells out to the Xberg CLI to extract text from a
// file. It returns an error when the binary is missing or extraction fails.
func extractWithXberg(ctx context.Context, path string) (string, error) {
	// Reconciliation installs the Xberg shim under STELLA_HOME; the daemon's
	// own PATH need not contain sandbox-only tool directories.
	stellaHome := config.StellaHome()
	bin := filepath.Join(pkgsandbox.MiseShimsDir(stellaHome), "xberg")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	managedShim := true
	if _, err := os.Stat(bin); err != nil {
		managedShim = false
		bin, err = exec.LookPath("xberg")
		if err != nil {
			return "", fmt.Errorf("xberg not available: %w", err)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, xbergTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "extract", path)
	// Xberg auto-discovers config from its cwd and parents. Anchor discovery to
	// the input file instead of leaking stellad's operator-controlled cwd.
	cmd.Dir = filepath.Dir(path)
	if managedShim {
		miseEnv := manifestplugins.RuntimeMiseEnv(stellaHome, "", "")
		// RuntimeMiseEnv uses the sandbox's /tmp by default; this command runs in
		// the daemon process, so use the host platform's temporary directory.
		miseEnv["MISE_STATE_DIR"] = filepath.Join(os.TempDir(), "stella-mise-state")
		cmd.Env = withEnvOverrides(os.Environ(), miseEnv)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("xberg returned no text")
	}
	return text, nil
}

// withEnvOverrides replaces environment values while retaining the process
// environment required by external tools (for example, PATH and certificates).
func withEnvOverrides(env []string, overrides map[string]string) []string {
	out := append([]string(nil), env...)
	for key, value := range overrides {
		prefix := key + "="
		found := false
		for i, entry := range out {
			if strings.HasPrefix(entry, prefix) {
				out[i] = prefix + value
				found = true
				break
			}
		}
		if !found {
			out = append(out, prefix+value)
		}
	}
	return out
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
