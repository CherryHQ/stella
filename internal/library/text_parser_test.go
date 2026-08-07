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
