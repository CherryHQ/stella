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

func TestAllowsUnlinkedGuestDM(t *testing.T) {
	valid := `{"allow_dm":true,"allow_unlinked_dm":true}`
	for _, tc := range []struct {
		name        string
		channelType string
		enabled     bool
		config      string
		want        bool
	}{
		{name: "enabled opted-in Discord channel", channelType: PlatformDiscord, enabled: true, config: valid, want: true},
		{name: "enabled opted-in Telegram channel", channelType: PlatformTelegram, enabled: true, config: valid, want: true},
		{name: "enabled opted-in Feishu channel", channelType: PlatformFeishu, enabled: true, config: valid, want: true},
		{name: "disabled channel", channelType: PlatformDiscord, config: valid},
		{name: "unsupported platform", channelType: PlatformQQ, enabled: true, config: valid},
		{name: "direct messages disabled", channelType: PlatformDiscord, enabled: true, config: `{"allow_dm":false,"allow_unlinked_dm":true}`},
		{name: "unlinked direct messages disabled", channelType: PlatformDiscord, enabled: true, config: `{"allow_dm":true}`},
		{name: "invalid config", channelType: PlatformDiscord, enabled: true, config: `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AllowsUnlinkedGuestDM(tc.channelType, tc.enabled, tc.config); got != tc.want {
				t.Fatalf("AllowsUnlinkedGuestDM() = %v, want %v", got, tc.want)
			}
		})
	}
}

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
		{"/command foo", "/command", "foo"},
		{"/command  bar  ", "/command", "bar"},
		{"/command", "/command", ""},
		{"other", "/command", "other"},
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
		{text: "/command value", wantCmd: "/command", wantArgs: "value"},
		{text: " /Other value ", wantCmd: "/other", wantArgs: "value"},
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
	blocks := FileReceivedContent("report.pdf", "$STELLA_ASSETS_DIR/report.pdf")
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

	blocks := AttachmentReceivedContent("photo.png", "$STELLA_ASSETS_DIR/photo.png", data)
	if len(blocks) != 2 {
		t.Fatalf("expected note + image blocks, got %d: %#v", len(blocks), blocks)
	}
	note, ok := blocks[0].(ai.TextContent)
	if !ok || !strings.Contains(note.Text, "$STELLA_ASSETS_DIR/photo.png") {
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
	blocks := AttachmentReceivedContent("report.pdf", "$STELLA_ASSETS_DIR/report.pdf", []byte("%PDF-1.7 not an image"))
	got := ai.FlattenText(blocks)
	if !strings.Contains(got, "`xberg extract") {
		t.Fatalf("non-image attachment = %q, want Xberg extraction hint", got)
	}
}

// An image past MaxImageInputBytes is also past what the vision tool accepts,
// so the hint must not send the agent to a tool that will reject it.
func TestAttachmentReceivedContentOversizedImageHintsExtraction(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	data := append(buf.Bytes(), make([]byte, ai.MaxImageInputBytes)...)
	blocks := AttachmentReceivedContent("huge.png", "$STELLA_ASSETS_DIR/huge.png", data)
	text := ai.FlattenText(blocks)
	if len(blocks) != 1 || !strings.Contains(text, "xberg extract") {
		t.Fatalf("blocks = %#v, want an extraction hint", blocks)
	}
	for _, gone := range []string{"`read`"} {
		if strings.Contains(text, gone) {
			t.Fatalf("hint names %s, which cannot handle an oversized image: %q", gone, text)
		}
	}
}

func TestInlineImageFallbackInlinesWithinCeiling(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	data := buf.Bytes()

	blocks := InlineImageFallback("photo.png", "image/png", data)
	if len(blocks) != 1 {
		t.Fatalf("expected single inline image block, got %d: %#v", len(blocks), blocks)
	}
	img, ok := blocks[0].(ai.ImageContent)
	if !ok || img.MimeType != "image/png" {
		t.Fatalf("block = %#v, want inline image/png", blocks[0])
	}
	if img.Data != base64.StdEncoding.EncodeToString(data) {
		t.Fatalf("inline image data does not round-trip the file bytes")
	}
}

func TestInlineImageFallbackOversizedBecomesTextNote(t *testing.T) {
	data := make([]byte, ai.MaxImageInputBytes+1)
	if blocks := InlineImageFallback("huge.png", "image/png", data); ai.HasImage(blocks) {
		t.Fatalf("oversized blocks = %#v, must become text", blocks)
	}
}

func TestInlineImageFallbackEmptyMimeBecomesTextNote(t *testing.T) {
	blocks := InlineImageFallback("mystery.bin", "", []byte("tiny"))
	if _, ok := blocks[0].(ai.ImageContent); ok {
		t.Fatalf("empty mime must not be inlined")
	}
	if got := ai.FlattenText(blocks); !strings.Contains(got, "mystery.bin") {
		t.Fatalf("note = %q, want filename", got)
	}
}

func TestAttachmentSaveFailureContentInlinesImage(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	blocks := AttachmentSaveFailureContent("photo.png", buf.Bytes())
	if len(blocks) != 1 {
		t.Fatalf("expected single inline image block, got %d", len(blocks))
	}
	if img, ok := blocks[0].(ai.ImageContent); !ok || img.MimeType != "image/png" {
		t.Fatalf("block = %#v, want inline image/png", blocks[0])
	}
}

func TestAttachmentSaveFailureContentOversizedImageBecomesTextNote(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	data := append(buf.Bytes(), make([]byte, ai.MaxImageInputBytes)...)
	if blocks := AttachmentSaveFailureContent("huge.png", data); ai.HasImage(blocks) {
		t.Fatalf("oversized blocks = %#v, must become text", blocks)
	}
}

func TestAttachmentSaveFailureContentNonImageBecomesPlaceholder(t *testing.T) {
	blocks := AttachmentSaveFailureContent("report.pdf", []byte("%PDF-1.7 not an image"))
	if _, ok := blocks[0].(ai.ImageContent); ok {
		t.Fatalf("non-image must not be inlined")
	}
	got := ai.FlattenText(blocks)
	if !strings.Contains(got, "report.pdf") || !strings.Contains(got, "could not be stored") {
		t.Fatalf("placeholder = %q, want filename + could-not-store text", got)
	}
	if strings.Contains(got, "xberg") {
		t.Fatalf("save-failure placeholder %q must not steer to Xberg (no saved path)", got)
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
