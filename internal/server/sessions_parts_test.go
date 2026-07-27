package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
)

func pngFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func filePart(url string) apitypes.MessagePart {
	return apitypes.MessagePart{Type: apitypes.File, Url: &url}
}

func textPart(text string) apitypes.MessagePart {
	return apitypes.MessagePart{Type: apitypes.Text, Text: &text}
}

func blocksOf(t *testing.T, content any) []ai.ContentBlock {
	t.Helper()
	blocks, ok := content.([]ai.ContentBlock)
	if !ok {
		t.Fatalf("content = %T, want []ai.ContentBlock", content)
	}
	return blocks
}

func TestPartsToMessageContentInlinesUploadedImage(t *testing.T) {
	data := pngFixture(t)
	const path = "/user/assets/202607/shot.png"
	read := func(_ context.Context, got string) ([]byte, error) {
		if got != path {
			t.Errorf("read path = %q, want %q", got, path)
		}
		return data, nil
	}

	blocks := blocksOf(t, partsToMessageContent(context.Background(), read,
		[]apitypes.MessagePart{filePart(path), textPart("what is this?")}))

	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want marker + image + text", len(blocks))
	}
	// The marker must survive: the transcript renders attachments from it and the
	// agent reaches the file through it.
	marker, ok := blocks[0].(ai.TextContent)
	if !ok || !strings.Contains(marker.Text, path) {
		t.Fatalf("first block = %#v, want a [file: path] marker", blocks[0])
	}
	img, ok := blocks[1].(ai.ImageContent)
	if !ok {
		t.Fatalf("second block = %T, want ai.ImageContent", blocks[1])
	}
	if img.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", img.MimeType)
	}
	if img.Data != base64.StdEncoding.EncodeToString(data) {
		t.Error("inlined bytes do not match the uploaded file")
	}
}

func TestPartsToMessageContentDetectsMimeFromBytesNotClient(t *testing.T) {
	// A client claiming "text/plain" for real PNG bytes must still be inlined as
	// an image: the declared type is advisory.
	part := filePart("/user/assets/202607/shot.png")
	mime := "text/plain"
	part.MimeType = &mime
	data := pngFixture(t)

	blocks := blocksOf(t, partsToMessageContent(context.Background(),
		func(context.Context, string) ([]byte, error) { return data, nil },
		[]apitypes.MessagePart{part}))

	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want marker + image", len(blocks))
	}
	if _, ok := blocks[1].(ai.ImageContent); !ok {
		t.Fatalf("second block = %T, want ai.ImageContent", blocks[1])
	}
}

func TestPartsToMessageContentKeepsMarkerForNonImage(t *testing.T) {
	blocks := blocksOf(t, partsToMessageContent(context.Background(),
		func(context.Context, string) ([]byte, error) { return []byte("%PDF-1.7\n"), nil },
		[]apitypes.MessagePart{filePart("/user/assets/202607/report.pdf")}))

	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want the marker alone so the Xberg hint still applies", len(blocks))
	}
	if _, ok := blocks[0].(ai.TextContent); !ok {
		t.Fatalf("block = %T, want ai.TextContent", blocks[0])
	}
}

func TestPartsToMessageContentKeepsMarkerWhenReadFails(t *testing.T) {
	// An attachment the API cannot serve must not fail the send: the path may
	// still resolve for the agent's own tools.
	blocks := blocksOf(t, partsToMessageContent(context.Background(),
		func(context.Context, string) ([]byte, error) { return nil, errors.New("gone") },
		[]apitypes.MessagePart{filePart("/user/assets/202607/shot.png"), textPart("hi")}))

	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want marker + text", len(blocks))
	}
	for _, b := range blocks {
		if _, ok := b.(ai.ImageContent); ok {
			t.Fatal("an unreadable upload must not produce an image block")
		}
	}
}

func TestPartsToMessageContentSkipsOversizedImage(t *testing.T) {
	// Past the inline ceiling the pixels are dropped, leaving the marker so the
	// read tool can apply its own resize.
	oversized := make([]byte, vision.MaxInlineBytes+1)
	copy(oversized, pngFixture(t))

	blocks := blocksOf(t, partsToMessageContent(context.Background(),
		func(context.Context, string) ([]byte, error) { return oversized, nil },
		[]apitypes.MessagePart{filePart("/user/assets/202607/huge.png")}))

	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want the marker alone", len(blocks))
	}
}

func TestPartsToMessageContentPlainTextStaysAString(t *testing.T) {
	content := partsToMessageContent(context.Background(), nil,
		[]apitypes.MessagePart{textPart("just words")})
	if got, ok := content.(string); !ok || got != "just words" {
		t.Fatalf("content = %#v, want the plain string unchanged", content)
	}
}
