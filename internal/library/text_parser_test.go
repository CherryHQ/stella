package library

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTextParserChunksUnicodeRunesWithStableByteLocators(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("a", 999) + "你" + "好"
	path := writeTextParserFixture(t, content)

	chunks, err := NewTextParser().Parse(t.Context(), path, MediaTypeText)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if got := utf8.RuneCountInString(chunks[0].Content); got != TextChunkRunes {
		t.Fatalf("first chunk runes = %d, want %d", got, TextChunkRunes)
	}
	if got := utf8.RuneCountInString(chunks[1].Content); got != TextChunkOverlapRunes+1 {
		t.Fatalf("second chunk runes = %d, want %d", got, TextChunkOverlapRunes+1)
	}
	if chunks[0].Locator.ByteStart != 0 || chunks[0].Locator.ByteEnd != 1002 {
		t.Fatalf("first locator = %+v, want bytes [0,1002)", chunks[0].Locator)
	}
	if chunks[1].Locator.ByteStart != 800 || chunks[1].Locator.ByteEnd != len(content) {
		t.Fatalf("second locator = %+v, want bytes [800,%d)", chunks[1].Locator, len(content))
	}
	firstRunes := []rune(chunks[0].Content)
	secondRunes := []rune(chunks[1].Content)
	if string(firstRunes[len(firstRunes)-TextChunkOverlapRunes:]) != string(secondRunes[:TextChunkOverlapRunes]) {
		t.Fatal("adjacent chunks do not share the configured rune overlap")
	}
}

func TestTextParserRetainsSymbolOnlyWindowsInsideSearchableDocument(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("a", 900) + strings.Repeat("★", 2_000) + strings.Repeat("b", 900)
	path := writeTextParserFixture(t, content)

	chunks, err := NewTextParser().Parse(t.Context(), path, MediaTypeText)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 5 {
		t.Fatalf("chunks = %d, want 5", len(chunks))
	}
	if chunks[2].Content != strings.Repeat("★", TextChunkRunes) {
		t.Fatal("symbol-only middle window was not retained")
	}
	if chunks[2].Locator.ByteStart != 3_000 || chunks[2].Locator.ByteEnd != 6_000 {
		t.Fatalf("symbol-only locator = %+v, want bytes [3000,6000)", chunks[2].Locator)
	}
	for index := 1; index < len(chunks); index++ {
		if chunks[index].Locator.ByteStart > chunks[index-1].Locator.ByteEnd {
			t.Fatalf("source gap between chunks %d and %d: %+v then %+v", index-1, index, chunks[index-1].Locator, chunks[index].Locator)
		}
	}
}

func TestTextParserTreatsMarkdownAsPlainText(t *testing.T) {
	t.Parallel()
	path := writeTextParserFixture(t, "# Travel\n\nApproval is required.")
	chunks, err := NewTextParser().Parse(t.Context(), path, MediaTypeMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Content != "# Travel\n\nApproval is required." {
		t.Fatalf("Markdown chunks = %+v", chunks)
	}
	if _, err := NewTextParser().Parse(t.Context(), path, "application/pdf"); !errors.Is(err, ErrUnsupportedFileType) {
		t.Fatalf("unsupported media error = %v, want ErrUnsupportedFileType", err)
	}
}

func TestTextParserRejectsInvalidOrIneffectiveText(t *testing.T) {
	t.Parallel()
	invalidPath := writeTextParserBytes(t, []byte{0xff})
	if _, err := NewTextParser().Parse(t.Context(), invalidPath, MediaTypeText); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("invalid UTF-8 error = %v, want ErrInvalidFile", err)
	}
	punctuationPath := writeTextParserFixture(t, " --- \n")
	if _, err := NewTextParser().Parse(t.Context(), punctuationPath, MediaTypeText); !errors.Is(err, ErrNoExtractedText) {
		t.Fatalf("ineffective text error = %v, want ErrNoExtractedText", err)
	}
	symbolPath := writeTextParserFixture(t, strings.Repeat("★", TextChunkRunes))
	if _, err := NewTextParser().Parse(t.Context(), symbolPath, MediaTypeText); !errors.Is(err, ErrNoExtractedText) {
		t.Fatalf("symbol-only document error = %v, want ErrNoExtractedText", err)
	}
}

func TestTextParserOmitsWhitespaceOnlyTail(t *testing.T) {
	t.Parallel()
	path := writeTextParserFixture(t, strings.Repeat("a", TextChunkRunes)+"\n\n\n")

	chunks, err := NewTextParser().Parse(t.Context(), path, MediaTypeText)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Content != strings.Repeat("a", TextChunkRunes) {
		t.Fatalf("whitespace-only tail produced chunks = %+v", chunks)
	}
}

func TestTextParserHonorsCancellationAndPinnedLimits(t *testing.T) {
	t.Parallel()
	path := writeTextParserFixture(t, "content")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewTextParser().Parse(ctx, path, MediaTypeText); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled parse error = %v, want context cancellation", err)
	}
	if MaxFileBytes != 25<<20 || TextChunkRunes != 1_000 || TextChunkOverlapRunes != 200 ||
		MaxParsedChunks != 32_768 || MaxParsedChunkContentBytes != 40<<20 ||
		MaxStagedChunksPerTx != 500 || MaxStagedContentBytesPerTx != 2<<20 {
		t.Fatal("TXT/Markdown parser limits drifted from the approved V1 profile")
	}
	const wantProfile = "builtin-text:v1;unit=unicode-rune;size=1000;overlap=200;markdown=plain;max_input_bytes=26214400;max_chunks=32768;max_content_bytes=41943040"
	if TextParserProfile != wantProfile {
		t.Fatalf("TextParserProfile = %q, want %q", TextParserProfile, wantProfile)
	}
}

func TestTextParserAcceptsMaximumSizeASCIIInput(t *testing.T) {
	stride := TextChunkRunes - TextChunkOverlapRunes
	if stride <= 0 {
		t.Fatalf("text chunk stride = %d, want positive", stride)
	}
	maxASCIIChunks := 1
	if MaxFileBytes > TextChunkRunes {
		maxASCIIChunks += (MaxFileBytes - TextChunkRunes + stride - 1) / stride
	}
	if maxASCIIChunks > MaxParsedChunks {
		t.Fatalf(
			"maximum accepted ASCII input needs %d chunks, exceeding limit %d",
			maxASCIIChunks,
			MaxParsedChunks,
		)
	}
	path := writeTextParserFixture(t, strings.Repeat("a", MaxFileBytes))
	chunks, err := NewTextParser().Parse(t.Context(), path, MediaTypeText)
	if err != nil {
		t.Fatalf("parse maximum-size ASCII input: %v", err)
	}
	if len(chunks) != maxASCIIChunks {
		t.Fatalf("maximum-size ASCII chunks = %d, want %d", len(chunks), maxASCIIChunks)
	}
}

func writeTextParserFixture(t *testing.T, content string) string {
	t.Helper()
	return writeTextParserBytes(t, []byte(content))
}

func writeTextParserBytes(t *testing.T, content []byte) string {
	t.Helper()
	path := t.TempDir() + "/source.txt"
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
