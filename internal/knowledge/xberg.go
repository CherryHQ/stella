// Package knowledge implements file-backed knowledge ingestion and retrieval.
package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// XbergVersion is the parser version whose CLI and JSON contract are covered
	// by the adapter tests.
	XbergVersion = "1.0.0-rc.35"

	DefaultChunkSize       = 1000
	DefaultChunkOverlap    = 200
	DefaultParseTimeout    = 5 * time.Minute
	DefaultProbeTimeout    = 10 * time.Second
	DefaultMaxStdoutBytes  = 128 << 20
	DefaultMaxContentBytes = 32 << 20
	DefaultMaxChunkBytes   = 48 << 20
	DefaultMaxChunks       = 50_000
	DefaultMaxStderrBytes  = 1 << 10
)

var (
	ErrNoExtractedText  = errors.New("document contains no extractable text")
	ErrParseOutputLimit = errors.New("xberg output exceeds the configured limit")
	ErrInvalidXbergJSON = errors.New("xberg returned invalid JSON")
	ErrParseResultLimit = errors.New("xberg parse result exceeds the configured limit")
)

// ParsedChunk is the normalized parser result persisted as a knowledge chunk.
type ParsedChunk struct {
	Content string
	Locator ChunkLocator
}

// ChunkLocator records stable source positioning. Byte offsets are retained for
// internal diagnostics but are removed before a chunk is returned to a model.
type ChunkLocator struct {
	FirstPage   *uint32  `json:"first_page,omitempty"`
	LastPage    *uint32  `json:"last_page,omitempty"`
	HeadingPath []string `json:"heading_path,omitempty"`
	ByteStart   int      `json:"byte_start"`
	ByteEnd     int      `json:"byte_end"`
}

// XbergParserConfig controls parser process and resource limits.
type XbergParserConfig struct {
	Binary string
	// Environment overrides inherited variables for the managed Xberg process.
	Environment     map[string]string
	Timeout         time.Duration
	MaxStdoutBytes  int64
	MaxStderrBytes  int64
	MaxContentBytes int
	MaxChunkBytes   int
	MaxChunks       int
}

// DefaultXbergParserConfig returns the V1 parser limits from the approved
// Knowledge Base solution.
func DefaultXbergParserConfig(binary string) XbergParserConfig {
	return XbergParserConfig{
		Binary:          binary,
		Timeout:         DefaultParseTimeout,
		MaxStdoutBytes:  DefaultMaxStdoutBytes,
		MaxStderrBytes:  DefaultMaxStderrBytes,
		MaxContentBytes: DefaultMaxContentBytes,
		MaxChunkBytes:   DefaultMaxChunkBytes,
		MaxChunks:       DefaultMaxChunks,
	}
}

// XbergParser invokes the managed Xberg CLI and validates its JSON response.
type XbergParser struct {
	config XbergParserConfig
	runner xbergRunner
}

// NewXbergParser constructs a parser backed by a managed Xberg binary.
func NewXbergParser(config XbergParserConfig) (*XbergParser, error) {
	config = normalizeXbergParserConfig(config)
	if strings.TrimSpace(config.Binary) == "" {
		return nil, fmt.Errorf("xberg binary is required")
	}
	return &XbergParser{
		config: config,
		runner: execXbergRunner{env: xbergProcessEnvironment(config.Environment)},
	}, nil
}

// Available verifies both the managed executable and its packaged runtime
// before an upload is persisted. The short real process probe catches broken
// installations without consuming River retries.
func (p *XbergParser) Available() error {
	if p == nil {
		return fmt.Errorf("xberg parser is unavailable")
	}
	info, err := os.Stat(p.config.Binary)
	if err != nil {
		return fmt.Errorf("stat xberg binary: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("xberg binary path is a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("xberg binary is not executable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), DefaultProbeTimeout)
	defer cancel()
	var stdout limitedBuffer
	stdout.limit = DefaultMaxStderrBytes
	var stderr limitedBuffer
	stderr.limit = DefaultMaxStderrBytes
	if err := p.runner.Probe(ctx, p.config.Binary, &stdout, &stderr); err != nil {
		diagnostic := sanitizeXbergDiagnostic(stderr.String(), p.config.Binary)
		if diagnostic == "" {
			return fmt.Errorf("probe xberg runtime: %w", err)
		}
		return fmt.Errorf("probe xberg runtime: %w: %s", err, diagnostic)
	}
	wantVersion := "xberg " + XbergVersion
	if version := strings.TrimSpace(stdout.String()); version != wantVersion {
		return fmt.Errorf("unexpected xberg version %q; want %q", version, wantVersion)
	}
	return nil
}

func newXbergParserWithRunner(config XbergParserConfig, runner xbergRunner) (*XbergParser, error) {
	parser, err := NewXbergParser(config)
	if err != nil {
		return nil, err
	}
	if runner == nil {
		return nil, fmt.Errorf("xberg runner is required")
	}
	parser.runner = runner
	return parser, nil
}

func normalizeXbergParserConfig(config XbergParserConfig) XbergParserConfig {
	defaults := DefaultXbergParserConfig(config.Binary)
	if config.Timeout <= 0 {
		config.Timeout = defaults.Timeout
	}
	if config.MaxStdoutBytes <= 0 {
		config.MaxStdoutBytes = defaults.MaxStdoutBytes
	}
	if config.MaxStderrBytes <= 0 {
		config.MaxStderrBytes = defaults.MaxStderrBytes
	}
	if config.MaxContentBytes <= 0 {
		config.MaxContentBytes = defaults.MaxContentBytes
	}
	if config.MaxChunkBytes <= 0 {
		config.MaxChunkBytes = defaults.MaxChunkBytes
	}
	if config.MaxChunks <= 0 {
		config.MaxChunks = defaults.MaxChunks
	}
	return config
}

// Parse extracts and chunks one validated local document. The caller owns the
// temporary file and must remove it after Parse returns.
func (p *XbergParser) Parse(ctx context.Context, path, mediaType string) ([]ParsedChunk, error) {
	if p == nil || p.runner == nil {
		return nil, fmt.Errorf("xberg parser is unavailable")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("document path is required")
	}
	if strings.TrimSpace(mediaType) == "" {
		return nil, fmt.Errorf("document media type is required")
	}

	parseCtx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()

	var stdout limitedBuffer
	stdout.limit = p.config.MaxStdoutBytes
	var stderr limitedBuffer
	stderr.limit = p.config.MaxStderrBytes

	args, err := xbergExtractArgs(path, mediaType)
	if err != nil {
		return nil, err
	}
	runErr := p.runner.Run(parseCtx, p.config.Binary, path, args, &stdout, &stderr)
	if errors.Is(stdout.err, ErrParseOutputLimit) {
		return nil, ErrParseOutputLimit
	}
	if runErr != nil {
		if errors.Is(parseCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("xberg parse timed out: %w", context.DeadlineExceeded)
		}
		diagnostic := sanitizeXbergDiagnostic(
			stderr.String(),
			path,
			filepath.Dir(path),
		)
		if diagnostic == "" {
			return nil, fmt.Errorf("xberg process failed: %w", runErr)
		}
		return nil, fmt.Errorf("xberg process failed: %w: %s", runErr, diagnostic)
	}

	return p.decode(stdout.Bytes())
}

func xbergExtractArgs(path, mediaType string) ([]string, error) {
	configJSON, err := json.Marshal(map[string]any{
		"chunking": map[string]any{
			"max_chars":               DefaultChunkSize,
			"max_overlap":             DefaultChunkOverlap,
			"chunker_type":            "markdown",
			"prepend_heading_context": false,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode xberg config: %w", err)
	}

	return []string{
		"--log-level", "error",
		"extract", path,
		"--format", "json",
		"--mime-type", mediaType,
		"--config-json", string(configJSON),
		"--no-cache", "true",
		"--disable-ocr", "true",
		"--content-format", "markdown",
		"--extract-pages", "true",
		"--page-markers", "false",
		"--extract-images", "false",
		"--pdf-extract-images", "false",
		"--pdf-ocr-inline-images", "false",
	}, nil
}

type xbergEnvelope struct {
	Result xbergDocument `json:"result"`
}

type xbergDocument struct {
	Content string       `json:"content"`
	Chunks  []xbergChunk `json:"chunks"`
}

type xbergChunk struct {
	Content  string             `json:"content"`
	Metadata xbergChunkMetadata `json:"metadata"`
}

type xbergChunkMetadata struct {
	ByteStart      int                  `json:"byte_start"`
	ByteEnd        int                  `json:"byte_end"`
	ChunkIndex     int                  `json:"chunk_index"`
	TotalChunks    int                  `json:"total_chunks"`
	FirstPage      *uint32              `json:"first_page"`
	LastPage       *uint32              `json:"last_page"`
	HeadingContext *xbergHeadingContext `json:"heading_context"`
	HeadingPath    []string             `json:"heading_path"`
}

type xbergHeadingContext struct {
	Headings []xbergHeading `json:"headings"`
}

type xbergHeading struct {
	Level uint8  `json:"level"`
	Text  string `json:"text"`
}

func (p *XbergParser) decode(data []byte) ([]ParsedChunk, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var envelope xbergEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidXbergJSON, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if len(envelope.Result.Content) > p.config.MaxContentBytes {
		return nil, fmt.Errorf("%w: extracted content is too large", ErrParseResultLimit)
	}
	if !hasEffectiveText(envelope.Result.Content) || len(envelope.Result.Chunks) == 0 {
		return nil, ErrNoExtractedText
	}
	if len(envelope.Result.Chunks) > p.config.MaxChunks {
		return nil, fmt.Errorf("%w: too many chunks", ErrParseResultLimit)
	}

	chunks := make([]ParsedChunk, 0, len(envelope.Result.Chunks))
	totalBytes := 0
	for index, chunk := range envelope.Result.Chunks {
		content := strings.TrimSpace(chunk.Content)
		if !hasEffectiveText(content) {
			continue
		}
		if chunk.Metadata.ChunkIndex != index {
			return nil, fmt.Errorf("%w: unstable chunk order at index %d", ErrInvalidXbergJSON, index)
		}
		if chunk.Metadata.TotalChunks != len(envelope.Result.Chunks) {
			return nil, fmt.Errorf("%w: inconsistent total_chunks", ErrInvalidXbergJSON)
		}
		if chunk.Metadata.ByteStart < 0 || chunk.Metadata.ByteEnd < chunk.Metadata.ByteStart {
			return nil, fmt.Errorf("%w: invalid chunk byte offsets", ErrInvalidXbergJSON)
		}
		totalBytes += len(content)
		if totalBytes > p.config.MaxChunkBytes {
			return nil, fmt.Errorf("%w: chunk content is too large", ErrParseResultLimit)
		}
		chunks = append(chunks, ParsedChunk{
			Content: content,
			Locator: ChunkLocator{
				FirstPage:   chunk.Metadata.FirstPage,
				LastPage:    chunk.Metadata.LastPage,
				HeadingPath: normalizeHeadingPath(chunk.Metadata),
				ByteStart:   chunk.Metadata.ByteStart,
				ByteEnd:     chunk.Metadata.ByteEnd,
			},
		})
	}
	if len(chunks) == 0 {
		return nil, ErrNoExtractedText
	}
	return chunks, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidXbergJSON, err)
	}
	return fmt.Errorf("%w: multiple JSON values", ErrInvalidXbergJSON)
}

func normalizeHeadingPath(metadata xbergChunkMetadata) []string {
	path := metadata.HeadingPath
	if len(path) == 0 && metadata.HeadingContext != nil {
		path = make([]string, 0, len(metadata.HeadingContext.Headings))
		for _, heading := range metadata.HeadingContext.Headings {
			path = append(path, heading.Text)
		}
	}
	out := make([]string, 0, len(path))
	for _, heading := range path {
		if normalized := strings.TrimSpace(heading); normalized != "" {
			out = append(out, normalized)
		}
	}
	return out
}

func hasEffectiveText(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func sanitizeXbergDiagnostic(value string, sensitivePaths ...string) string {
	value = strings.ToValidUTF8(value, "")
	for _, sensitivePath := range sensitivePaths {
		if sensitivePath != "" && sensitivePath != "." {
			value = strings.ReplaceAll(value, sensitivePath, "<temporary-path>")
		}
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= DefaultMaxStderrBytes {
		return value
	}
	limit := DefaultMaxStderrBytes
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}

type xbergRunner interface {
	Run(ctx context.Context, binary, documentPath string, args []string, stdout, stderr io.Writer) error
	Probe(ctx context.Context, binary string, stdout, stderr io.Writer) error
}

type execXbergRunner struct {
	env []string
}

func (r execXbergRunner) Run(
	ctx context.Context,
	binary string,
	documentPath string,
	args []string,
	stdout, stderr io.Writer,
) error {
	cmd := exec.CommandContext(ctx, binary, args...)
	// The Worker creates one controlled directory per parse. Using it as the
	// process cwd prevents Xberg from discovering an unrelated parent config.
	cmd.Dir = filepath.Dir(documentPath)
	cmd.Env = r.env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (r execXbergRunner) Probe(
	ctx context.Context,
	binary string,
	stdout, stderr io.Writer,
) error {
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.Dir = filepath.Dir(binary)
	cmd.Env = r.env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func xbergProcessEnvironment(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return nil
	}

	environment := make(map[string]string, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[key] = value
		}
	}
	maps.Copy(environment, overrides)

	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, key+"="+environment[key])
	}
	return entries
}

type limitedBuffer struct {
	bytes.Buffer
	limit int64
	err   error
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	remaining := b.limit - int64(b.Len())
	if remaining <= 0 {
		b.err = ErrParseOutputLimit
		return 0, b.err
	}
	if int64(len(p)) > remaining {
		n, _ := b.Buffer.Write(p[:remaining])
		b.err = ErrParseOutputLimit
		return n, b.err
	}
	return b.Buffer.Write(p)
}
