package library

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// TextParserProfile is persisted with every generation. Any setting that can
	// change chunk bytes or locators must produce a new profile value.
	TextParserProfile = "builtin-text:v1;unit=unicode-rune;size=1000;overlap=200;markdown=plain;max_input_bytes=26214400;max_chunks=32768;max_content_bytes=41943040"

	TextChunkRunes             = 1_000
	TextChunkOverlapRunes      = 200
	textCancellationCheckRunes = 256
	MaxParsedChunks            = 32_768
	MaxParsedChunkContentBytes = 40 << 20
	MaxStagedChunksPerTx       = 500
	MaxStagedContentBytesPerTx = 2 << 20
)

var (
	ErrNoExtractedText   = errors.New("document contains no extractable text")
	ErrInvalidParserData = errors.New("parser returned invalid chunk data")
	ErrParseResultLimit  = errors.New("parser result exceeds the configured limit")
)

// ParsedChunk is the normalized parser result persisted as a Library chunk.
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

// TextParser deterministically chunks UTF-8 text and Markdown without an
// external runtime. Markdown is intentionally treated as plain source text in
// V1; richer format-specific parsing can introduce a separate profile later.
type TextParser struct{}

func NewTextParser() *TextParser { return &TextParser{} }

func (*TextParser) Parse(ctx context.Context, filePath, mediaType string) ([]ParsedChunk, error) {
	if strings.TrimSpace(filePath) == "" {
		return nil, fmt.Errorf("document path is required")
	}
	if mediaType != MediaTypeText && mediaType != MediaTypeMarkdown {
		return nil, fmt.Errorf("%w: media type %q", ErrUnsupportedFileType, mediaType)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("inspect text source: %w", err)
	}
	if info.Size() == 0 {
		return nil, ErrNoExtractedText
	}
	if info.Size() > MaxFileBytes {
		return nil, fmt.Errorf("%w: maximum is %d bytes", ErrFileTooLarge, MaxFileBytes)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open text source: %w", err)
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReader(file)
	window := make([]rune, 0, TextChunkRunes)
	chunks := make([]ParsedChunk, 0, 32)
	byteStart := 0
	byteEnd := 0
	newRunes := 0
	totalContentBytes := 0
	hasDocumentText := false
	runesSinceCancellationCheck := 0

	emit := func() error {
		content := string(window)
		if len(chunks) >= MaxParsedChunks {
			return fmt.Errorf("%w: too many chunks", ErrParseResultLimit)
		}
		totalContentBytes += len(content)
		if totalContentBytes > MaxParsedChunkContentBytes {
			return fmt.Errorf("%w: chunk content is too large", ErrParseResultLimit)
		}
		chunks = append(chunks, ParsedChunk{
			Content: content,
			Locator: ChunkLocator{ByteStart: byteStart, ByteEnd: byteEnd},
		})
		return nil
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for {
		r, size, err := reader.ReadRune()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read text source: %w", err)
		}
		if r == '\x00' || r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("%w: text must be valid UTF-8 without NUL bytes", ErrInvalidFile)
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			hasDocumentText = true
		}
		runesSinceCancellationCheck++
		if runesSinceCancellationCheck == textCancellationCheckRunes {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			runesSinceCancellationCheck = 0
		}

		window = append(window, r)
		byteEnd += size
		newRunes++
		if len(window) < TextChunkRunes {
			continue
		}
		if err := emit(); err != nil {
			return nil, err
		}

		overlap := append([]rune(nil), window[len(window)-TextChunkOverlapRunes:]...)
		byteStart = byteEnd - len(string(overlap))
		window = overlap
		newRunes = 0
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A full final window was already emitted. A whitespace-only suffix does not
	// need a separate overlapping chunk; every non-whitespace source rune remains
	// covered by an emitted window.
	if newRunes > 0 && strings.TrimSpace(string(window[len(window)-newRunes:])) != "" {
		if err := emit(); err != nil {
			return nil, err
		}
	}
	// Effectiveness is a document-level decision. Individual symbol-only windows
	// are retained so diagrams, emoji, and mathematical notation do not leave
	// holes between otherwise searchable text chunks.
	if !hasDocumentText || len(chunks) == 0 {
		return nil, ErrNoExtractedText
	}
	return chunks, nil
}

func hasEffectiveText(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}
