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
	"github.com/CherryHQ/stella/internal/agent"
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
	return apitypes.MessagePart{Type: apitypes.MessagePartTypeFile, Url: &url}
}

func textPart(text string) apitypes.MessagePart {
	return apitypes.MessagePart{Type: apitypes.MessagePartTypeText, Text: &text}
}

func imagePart(data string) apitypes.MessagePart {
	return apitypes.MessagePart{Type: apitypes.MessagePartTypeImage, Image: &data}
}

func blocksOf(t *testing.T, content any) []ai.ContentBlock {
	t.Helper()
	blocks, ok := content.([]ai.ContentBlock)
	if !ok {
		t.Fatalf("content = %T, want []ai.ContentBlock", content)
	}
	return blocks
}

func mustPartsToMessageContent(t *testing.T, read workspaceRawReader, parts []apitypes.MessagePart) agent.MessageContent {
	t.Helper()
	content, err := partsToMessageContent(context.Background(), read, parts)
	if err != nil {
		t.Fatalf("partsToMessageContent: %v", err)
	}
	return content
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

	blocks := blocksOf(t, mustPartsToMessageContent(t, read, []apitypes.MessagePart{filePart(path), textPart("what is this?")}))
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want marker + image + text", len(blocks))
	}
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
	part := filePart("/user/assets/202607/shot.png")
	mime := "text/plain"
	part.MimeType = &mime
	blocks := blocksOf(t, mustPartsToMessageContent(t,
		func(context.Context, string) ([]byte, error) { return pngFixture(t), nil }, []apitypes.MessagePart{part}))
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want marker + image", len(blocks))
	}
	if _, ok := blocks[1].(ai.ImageContent); !ok {
		t.Fatalf("second block = %T, want ai.ImageContent", blocks[1])
	}
}

func TestPartsToMessageContentKeepsMarkerForNonImage(t *testing.T) {
	blocks := blocksOf(t, mustPartsToMessageContent(t,
		func(context.Context, string) ([]byte, error) { return []byte("%PDF-1.7\n"), nil },
		[]apitypes.MessagePart{filePart("/user/assets/202607/report.pdf")}))
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want marker alone", len(blocks))
	}
	if _, ok := blocks[0].(ai.TextContent); !ok {
		t.Fatalf("block = %T, want ai.TextContent", blocks[0])
	}
}

func TestPartsToMessageContentKeepsMarkerWhenReadFails(t *testing.T) {
	blocks := blocksOf(t, mustPartsToMessageContent(t,
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

func TestPartsToMessageContentKeepsCanonicalLargeImage(t *testing.T) {
	oversized := make([]byte, vision.MaxInlineBytes+1)
	copy(oversized, pngFixture(t))
	blocks := blocksOf(t, mustPartsToMessageContent(t,
		func(context.Context, string) ([]byte, error) { return oversized, nil },
		[]apitypes.MessagePart{filePart("/user/assets/202607/huge.png")}))
	if len(blocks) != 2 || !ai.HasImage(blocks) {
		t.Fatalf("blocks = %#v, want marker plus canonical image", blocks)
	}
}

func TestPartsToMessageContentRejectsTooManyWorkspaceImages(t *testing.T) {
	parts := make([]apitypes.MessagePart, 9)
	for i := range parts {
		parts[i] = filePart("/user/assets/image.png")
	}
	_, err := partsToMessageContent(context.Background(), func(context.Context, string) ([]byte, error) {
		return pngFixture(t), nil
	}, parts)
	if err == nil || !strings.Contains(err.Error(), "too many images") {
		t.Fatalf("error = %v, want image count rejection", err)
	}
}

func TestPartsToMessageContentRejectsWorkspaceImageAggregate(t *testing.T) {
	data := make([]byte, 21*1024*1024)
	copy(data, pngFixture(t))
	parts := []apitypes.MessagePart{filePart("/user/assets/one.png"), filePart("/user/assets/two.png"), filePart("/user/assets/three.png")}
	_, err := partsToMessageContent(context.Background(), func(context.Context, string) ([]byte, error) { return data, nil }, parts)
	if err == nil || !strings.Contains(err.Error(), "image inputs exceed") {
		t.Fatalf("error = %v, want aggregate rejection", err)
	}
}

func TestPartsToMessageContentRejectsTooManyDirectImages(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(pngFixture(t))
	parts := make([]apitypes.MessagePart, 9)
	for i := range parts {
		parts[i] = imagePart(encoded)
	}
	_, err := partsToMessageContent(context.Background(), nil, parts)
	if err == nil || !strings.Contains(err.Error(), "too many images") {
		t.Fatalf("error = %v, want direct image count rejection", err)
	}
}

func TestPartsToMessageContentRejectsTooManyPartsBeforeWorkspaceReads(t *testing.T) {
	parts := make([]apitypes.MessagePart, maxSessionMessageParts+1)
	for i := range parts {
		parts[i] = filePart("/user/assets/attachment.txt")
	}
	reads := 0
	_, err := partsToMessageContent(context.Background(), func(context.Context, string) ([]byte, error) {
		reads++
		return nil, nil
	}, parts)
	if err == nil || !strings.Contains(err.Error(), "too many message parts") {
		t.Fatalf("error = %v, want part limit rejection", err)
	}
	if reads != 0 {
		t.Fatalf("workspace reads = %d, want 0", reads)
	}
}

func TestPartsToMessageContentPlainTextStaysAString(t *testing.T) {
	content := mustPartsToMessageContent(t, nil, []apitypes.MessagePart{textPart("just words")})
	if got, ok := content.(string); !ok || got != "just words" {
		t.Fatalf("content = %#v, want the plain string unchanged", content)
	}
}
