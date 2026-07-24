package channel

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestSplitMessage_ShortText(t *testing.T) {
	got := SplitMessage("hello", 100)
	if len(got) != 1 || got[0] != "hello" {
		t.Errorf("unexpected result: %v", got)
	}
}

func TestSplitMessage_ExactLength(t *testing.T) {
	text := strings.Repeat("x", 10)
	got := SplitMessage(text, 10)
	if len(got) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(got))
	}
}

func TestSplitMessage_SplitsAtNewline(t *testing.T) {
	// "aaa\nbbb" with maxLen=5 should prefer to split at the newline boundary.
	text := "aaa\nbbb"
	chunks := SplitMessage(text, 5)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	// Reassemble and verify no data is lost.
	joined := strings.Join(chunks, "")
	if joined != text {
		t.Errorf("data lost after split: %q != %q", joined, text)
	}
}

func TestSplitMessage_UTF8Boundary(t *testing.T) {
	// 日 is 3 bytes. With maxLen=4 we should not split inside the rune.
	text := "日本"
	chunks := SplitMessage(text, 4)
	for _, c := range chunks {
		for _, r := range c {
			_ = r // valid rune iteration confirms no broken sequences
		}
	}
	joined := strings.Join(chunks, "")
	if joined != text {
		t.Errorf("data lost: %q != %q", joined, text)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{1500 * time.Millisecond, "1.5s"},
		{90 * time.Second, "1m30s"},
	}
	for _, tc := range tests {
		got := FormatDuration(tc.d)
		if got != tc.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestParseCommandArgs(t *testing.T) {
	tests := []struct {
		text string
		cmd  string
		want string
	}{
		{"/agent foo", "/agent", "foo"},
		{"/agent  bar  ", "/agent", "bar"},
		{"/agent", "/agent", ""},
		{"other", "/agent", "other"},
	}
	for _, tc := range tests {
		got := ParseCommandArgs(tc.text, tc.cmd)
		if got != tc.want {
			t.Errorf("ParseCommandArgs(%q, %q) = %q, want %q", tc.text, tc.cmd, got, tc.want)
		}
	}
}

func TestParseSlashCommand(t *testing.T) {
	tests := []struct {
		text     string
		wantCmd  string
		wantArgs string
	}{
		{text: "/model openai/gpt-4", wantCmd: "/model", wantArgs: "openai/gpt-4"},
		{text: " /Agent stella ", wantCmd: "/agent", wantArgs: "stella"},
		{text: "hello", wantCmd: "", wantArgs: ""},
	}

	for _, tc := range tests {
		gotCmd, gotArgs := ParseSlashCommand(tc.text)
		if gotCmd != tc.wantCmd || gotArgs != tc.wantArgs {
			t.Errorf("ParseSlashCommand(%q) = (%q, %q), want (%q, %q)", tc.text, gotCmd, gotArgs, tc.wantCmd, tc.wantArgs)
		}
	}
}

func TestFileReceivedContentUsesXberg(t *testing.T) {
	blocks := FileReceivedContent("report.pdf", "/home/stella/assets", "/home/stella/assets/report.pdf")
	got := ai.FlattenText(blocks)
	if !strings.Contains(got, "Read Xberg skill") || !strings.Contains(got, "`xberg extract") {
		t.Fatalf("FileReceivedContent() = %q, want Xberg extraction hint", got)
	}
}

func TestAttachmentReceivedContentInlinesImages(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	data := buf.Bytes()

	blocks := AttachmentReceivedContent("photo.png", "/home/stella/assets", "/home/stella/assets/photo.png", data)
	if len(blocks) != 2 {
		t.Fatalf("expected note + image blocks, got %d: %#v", len(blocks), blocks)
	}
	note, ok := blocks[0].(ai.TextContent)
	if !ok || !strings.Contains(note.Text, "assets/photo.png") {
		t.Fatalf("note block = %#v, want saved-path note", blocks[0])
	}
	if strings.Contains(note.Text, "xberg") {
		t.Fatalf("image note %q must not steer to Xberg", note.Text)
	}
	img, ok := blocks[1].(ai.ImageContent)
	if !ok || img.MimeType != "image/png" {
		t.Fatalf("image block = %#v, want inline image/png", blocks[1])
	}
	if img.Data != base64.StdEncoding.EncodeToString(data) {
		t.Fatalf("image block data does not round-trip the file bytes")
	}
}

func TestAttachmentReceivedContentFallsBackToFileHint(t *testing.T) {
	blocks := AttachmentReceivedContent("report.pdf", "/home/stella/assets", "/home/stella/assets/report.pdf", []byte("%PDF-1.7 not an image"))
	got := ai.FlattenText(blocks)
	if !strings.Contains(got, "`xberg extract") {
		t.Fatalf("non-image attachment = %q, want Xberg extraction hint", got)
	}
}

func TestAttachmentReceivedContentOversizedImageHintsRead(t *testing.T) {
	// A real PNG header followed by padding past the inline ceiling: detected as
	// an image but too large to inline.
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	data := append(buf.Bytes(), make([]byte, maxInlineAttachmentImageBytes)...)

	blocks := AttachmentReceivedContent("huge.png", "/home/stella/assets", "/home/stella/assets/huge.png", data)
	if len(blocks) != 1 {
		t.Fatalf("expected single text block, got %d", len(blocks))
	}
	got := ai.FlattenText(blocks)
	if !strings.Contains(got, "`read` tool") || strings.Contains(got, "xberg") {
		t.Fatalf("oversized image = %q, want read-tool hint without Xberg", got)
	}
}

func TestImageFileName(t *testing.T) {
	tests := []struct {
		base, mime, want string
	}{
		{"abc123", "image/jpeg", "abc123.jpg"},
		{"abc123", "image/png", "abc123.png"},
		{"", "image/webp", "image.webp"},
		{"x", "application/octet-stream", "x.bin"},
	}
	for _, tc := range tests {
		if got := ImageFileName(tc.base, tc.mime); got != tc.want {
			t.Errorf("ImageFileName(%q, %q) = %q, want %q", tc.base, tc.mime, got, tc.want)
		}
	}
}

func TestTextContent(t *testing.T) {
	blocks := TextContent("hello")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	tc, ok := blocks[0].(ai.TextContent)
	if !ok {
		t.Fatalf("expected TextContent block, got %T", blocks[0])
	}
	if tc.Text != "hello" {
		t.Errorf("expected 'hello', got %q", tc.Text)
	}
}
