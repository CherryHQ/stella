package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	xbergAdapterContractVersion = "v4"
	xbergTimeout                = 60 * time.Second
	xbergStdoutLimit            = 48 << 20
	xbergStderrLimit            = 64 << 10
	xbergChunkingConfigJSON     = `{"chunking":{"max_chars":1000,"max_overlap":200,"trim":true,"chunker_type":"markdown","table_chunking":"repeat_header"}}`
	xbergPresentationConfigJSON = `{"pages":{"extract_pages":true,"insert_page_markers":true,"marker_format":"\n\n<!-- STELLA_LIBRARY_SLIDE {page_num} -->\n\n"},"chunking":{"max_chars":1000,"max_overlap":200,"trim":true,"chunker_type":"markdown","table_chunking":"repeat_header"}}`
)

func xbergCanonicalArgs(mediaType string) []string {
	// The embedded runtime pins Xberg. Config JSON is necessary because the
	// --chunk convenience flags force the plain-text chunker and discard heading
	// context that Library needs for structured citations.
	configJSON := xbergChunkingConfigJSON
	pageMarkers := "false"
	if hasReliableSlideRanges(mediaType) {
		// Xberg's source page offsets predate its final Markdown rendering. A
		// private marker gives the adapter a stable slide boundary to remove.
		configJSON = xbergPresentationConfigJSON
		pageMarkers = "true"
	}
	return []string{
		"--no-config-discovery",
		"--disable-ocr", "true",
		"--quality", "false",
		"--force-ocr", "false",
		"--include-structure", "true",
		"--content-format", "markdown",
		"--extract-pages", "true",
		"--page-markers", pageMarkers,
		"--no-cache", "true",
		"--config-json", configJSON,
		"--format", "json",
	}
}

type xbergCommandRunner func(context.Context, string, []string) ([]byte, []byte, error)

type XbergCLIParser struct {
	binary, version string
	run             xbergCommandRunner
}

type xbergChunk struct {
	Content  string `json:"content"`
	Metadata struct {
		ByteStart      *int     `json:"byte_start"`
		ByteEnd        *int     `json:"byte_end"`
		FirstPage      *uint32  `json:"first_page"`
		LastPage       *uint32  `json:"last_page"`
		HeadingPath    []string `json:"heading_path"`
		HeadingContext *struct {
			Headings []struct {
				Text string `json:"text"`
			} `json:"headings"`
		} `json:"heading_context"`
	} `json:"metadata"`
}

type xbergTable struct {
	Cells      [][]string `json:"cells"`
	Markdown   string     `json:"markdown"`
	PageNumber uint32     `json:"page_number"`
	Columns    []string   `json:"columns"`
}

type xbergPage struct {
	PageNumber uint32       `json:"page_number"`
	Content    string       `json:"content"`
	SheetName  string       `json:"sheet_name"`
	Tables     []xbergTable `json:"tables"`
}

type xbergResult struct {
	Content string       `json:"content"`
	Chunks  []xbergChunk `json:"chunks"`
	Tables  []xbergTable `json:"tables"`
	Pages   []xbergPage  `json:"pages"`
}

func NewXbergCLIParser(ctx context.Context, binary string) (*XbergCLIParser, error) {
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("resolve Xberg CLI %q: %w", binary, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute Xberg CLI path: %w", err)
	}
	return newXbergCLIParser(ctx, resolved, runBoundedXbergCommand)
}

func newXbergCLIParser(ctx context.Context, binary string, run xbergCommandRunner) (*XbergCLIParser, error) {
	ctx, cancel := context.WithTimeout(ctx, xbergTimeout)
	defer cancel()
	stdout, _, runErr := run(ctx, binary, []string{"version", "--format", "json"})
	if runErr != nil {
		return nil, fmt.Errorf("probe Xberg CLI version: %w", runErr)
	}
	var envelope struct {
		Version string `json:"version"`
		Result  struct {
			Version string `json:"version"`
		} `json:"result"`
	}
	if err := decodeSingleJSON(stdout, &envelope); err != nil {
		return nil, fmt.Errorf("probe Xberg CLI version JSON: %w", err)
	}
	version := strings.TrimSpace(envelope.Version)
	if version == "" {
		version = strings.TrimSpace(envelope.Result.Version)
	}
	if version == "" {
		return nil, fmt.Errorf("probe Xberg CLI version JSON: version is missing")
	}
	formatsJSON, _, runErr := run(ctx, binary, []string{"formats", "--format", "json"})
	if runErr != nil {
		return nil, fmt.Errorf("probe Xberg CLI formats: %w", runErr)
	}
	var formats []struct {
		Extension string `json:"extension"`
		MediaType string `json:"mime_type"`
	}
	if err := decodeSingleJSON(formatsJSON, &formats); err != nil {
		return nil, fmt.Errorf("probe Xberg CLI formats JSON: %w", err)
	}
	available := make(map[string]struct{}, len(formats))
	availableMediaTypes := make(map[string]struct{}, len(formats))
	for _, format := range formats {
		available["."+strings.ToLower(strings.TrimSpace(format.Extension))] = struct{}{}
		availableMediaTypes[strings.TrimSpace(format.MediaType)] = struct{}{}
	}
	for _, mediaType := range XbergMediaTypes() {
		extension, err := canonicalExtension(mediaType)
		if err != nil {
			return nil, err
		}
		_, extensionAvailable := available[extension]
		_, mediaTypeAvailable := availableMediaTypes[mediaType]
		// Xberg detects XHTML as an alias of its HTML extractor even though the
		// formats command lists only .html/.htm as the canonical extensions.
		if mediaType == MediaTypeXHTML {
			_, mediaTypeAvailable = availableMediaTypes[MediaTypeHTML]
		}
		if !extensionAvailable && !mediaTypeAvailable {
			return nil, fmt.Errorf("probe Xberg CLI formats: required extension %s is unavailable", extension)
		}
	}
	return &XbergCLIParser{binary: binary, version: version, run: run}, nil
}

func (p *XbergCLIParser) Profile(mediaType string) (string, error) {
	if !isXbergMediaType(mediaType) {
		return "", fmt.Errorf("%w: media type %q", ErrUnsupportedFileType, mediaType)
	}
	argsHash := sha256.Sum256([]byte(strings.Join(xbergCanonicalArgs(mediaType), "\x00")))
	return "xberg-cli-adapter:" + xbergAdapterContractVersion + ";cli=" + p.version + ";args_sha256=" + hex.EncodeToString(argsHash[:]), nil
}

func (p *XbergCLIParser) Parse(ctx context.Context, path, mediaType string) ([]ParsedChunk, error) {
	if _, err := p.Profile(mediaType); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve Xberg input path: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, xbergTimeout)
	defer cancel()
	args := append([]string{"extract", absolute}, xbergCanonicalArgs(mediaType)...)
	stdout, _, runErr := p.run(ctx, p.binary, args)
	if runErr != nil {
		return nil, fmt.Errorf("run Xberg extraction: %w", runErr)
	}
	var envelope struct {
		Result xbergResult `json:"result"`
	}
	if err := decodeSingleJSON(stdout, &envelope); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidParserData, err)
	}
	if isSpreadsheetMediaType(mediaType) {
		chunks, err := structuredTableChunks(envelope.Result, mediaType)
		if err != nil {
			return nil, err
		}
		if len(chunks) > 0 {
			return chunks, nil
		}
	}
	if len(envelope.Result.Chunks) == 0 {
		return nil, ErrNoExtractedText
	}
	chunks := make([]ParsedChunk, len(envelope.Result.Chunks))
	for i, chunk := range envelope.Result.Chunks {
		if chunk.Metadata.ByteStart == nil || chunk.Metadata.ByteEnd == nil || *chunk.Metadata.ByteEnd <= *chunk.Metadata.ByteStart {
			return nil, fmt.Errorf("%w: Xberg chunk %d has missing or invalid byte offsets", ErrInvalidParserData, i)
		}
		headingPath := append([]string(nil), chunk.Metadata.HeadingPath...)
		if len(headingPath) == 0 && chunk.Metadata.HeadingContext != nil {
			for _, heading := range chunk.Metadata.HeadingContext.Headings {
				if text := strings.TrimSpace(heading.Text); text != "" {
					headingPath = append(headingPath, text)
				}
			}
		}
		// DOC headings are heuristic, while YAML and TOML do not yet expose a
		// stable structural path. Keep their public citations filename-only.
		if mediaType == MediaTypeDOC || mediaType == MediaTypePPT || mediaType == MediaTypeODP ||
			mediaType == MediaTypeYAML || mediaType == MediaTypeTOML {
			headingPath = nil
		}
		// Xberg 1.0.14 can place multiple Markdown headings in one chunk while
		// reporting only the first heading as its context. Keep the text
		// searchable, but do not expose a heading citation that may be false.
		if containsMultipleMarkdownHeadings(chunk.Content) {
			headingPath = nil
		}
		firstPage, lastPage := chunk.Metadata.FirstPage, chunk.Metadata.LastPage
		if mediaType != MediaTypePDF && !hasReliableSlideRanges(mediaType) {
			// Xberg may assign internal page-like coordinates to narrative and
			// ebook formats. They are not stable rendered page numbers, so only
			// PDF and presentation routes expose them as public citations.
			firstPage, lastPage = nil, nil
		}
		chunks[i] = ParsedChunk{Content: chunk.Content, Locator: ChunkLocator{
			ByteStart: *chunk.Metadata.ByteStart, ByteEnd: *chunk.Metadata.ByteEnd,
			FirstPage: firstPage, LastPage: lastPage,
			HeadingPath: headingPath,
		}}
	}
	if hasReliableSlideRanges(mediaType) {
		return enforcePresentationPageBoundaries(chunks)
	}
	return chunks, nil
}

func containsMultipleMarkdownHeadings(content string) bool {
	headings := 0
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 3 || trimmed[0] != '#' {
			continue
		}
		markerEnd := 0
		for markerEnd < len(trimmed) && trimmed[markerEnd] == '#' {
			markerEnd++
		}
		if markerEnd > 6 || markerEnd == len(trimmed) || trimmed[markerEnd] != ' ' {
			continue
		}
		headings++
		if headings > 1 {
			return true
		}
	}
	return false
}

type cappedBuffer struct {
	bytes.Buffer
	max      int
	exceeded bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		b.exceeded = true
		allowed := b.max - b.Len()
		if allowed > 0 {
			_, _ = b.Buffer.Write(p[:allowed])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func runBoundedXbergCommand(ctx context.Context, binary string, args []string) ([]byte, []byte, error) {
	stdout := &cappedBuffer{max: xbergStdoutLimit}
	stderr := &cappedBuffer{max: xbergStderrLimit}
	cmd := exec.CommandContext(ctx, binary, args...)
	// Xberg inputs are absolute and config discovery is disabled. Its official
	// Linux and macOS bundles resolve adjacent dynamic libraries from this dir.
	cmd.Dir = filepath.Dir(binary)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if stdout.exceeded {
		return nil, stderr.Bytes(), fmt.Errorf("%w: Xberg stdout exceeds %d bytes", ErrParseResultLimit, xbergStdoutLimit)
	}
	if stderr.exceeded {
		return nil, stderr.Bytes(), fmt.Errorf("xberg stderr exceeds %d bytes", xbergStderrLimit)
	}
	if err != nil {
		return nil, stderr.Bytes(), err
	}
	return stdout.Bytes(), stderr.Bytes(), nil
}

func decodeSingleJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}
