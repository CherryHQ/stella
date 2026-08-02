package ai

import (
	"errors"
	"testing"
)

func TestContentBlocksJSONRoundTrip(t *testing.T) {
	in := []ContentBlock{
		TextContent{Text: "look at this"},
		ImageContent{Data: "aGVsbG8=", MimeType: "image/png"},
	}

	data, err := MarshalContentBlocks(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := UnmarshalContentBlocks(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("blocks = %d, want 2", len(out))
	}
	if tc, ok := out[0].(TextContent); !ok || tc.Text != "look at this" {
		t.Errorf("block 0 = %#v, want original text", out[0])
	}
	if ic, ok := out[1].(ImageContent); !ok || ic.Data != "aGVsbG8=" || ic.MimeType != "image/png" {
		t.Errorf("block 1 = %#v, want original image", out[1])
	}
}

func TestUnmarshalContentBlocksEmptyFallsBack(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("[]")} {
		blocks, err := UnmarshalContentBlocks(data)
		if err != nil {
			t.Fatalf("unmarshal %q: %v", data, err)
		}
		if blocks != nil {
			t.Errorf("unmarshal %q = %#v, want nil for text fallback", data, blocks)
		}
	}
}

func TestCanonicalContentBlocksRejectRawImageAndAcceptRef(t *testing.T) {
	err := ValidateCanonicalContentBlocks([]ContentBlock{ImageContent{Data: "aGVsbG8=", MimeType: "image/png"}})
	if !errors.Is(err, ErrRawImageContent) {
		t.Fatalf("raw image error = %v, want ErrRawImageContent", err)
	}
	blocks := []ContentBlock{TextContent{Text: "look"}, ImageRefContent{
		MediaID:  "018fbc12-e8dc-7502-916d-46c695202d62",
		Baseline: ImageBaseline{Text: "## Text\nhello\n\n## Scene\na chart"},
	}}
	if err := ValidateCanonicalContentBlocks(blocks); err != nil {
		t.Fatalf("ValidateCanonicalContentBlocks: %v", err)
	}
}

func TestImageRefRejectsInvalidBaseline(t *testing.T) {
	for _, text := range []string{
		"not a baseline",
		"## Text\nhello\n\n## Scene\na chart\n\n## Details\nmore",
	} {
		ref := ImageRefContent{MediaID: "media", Baseline: ImageBaseline{Text: text}}
		if err := ref.Validate(); err == nil {
			t.Fatal("ImageRefContent.Validate unexpectedly accepted invalid baseline")
		}
		if err := ValidateCanonicalContentBlocks([]ContentBlock{ref}); err == nil {
			t.Fatal("ValidateCanonicalContentBlocks unexpectedly accepted invalid baseline")
		}
	}
}

func TestImageBaselineTextAllowsOCRHeadingsBeforeScene(t *testing.T) {
	text := "## Text\nInvoice\n\n## Line Items\nWidget\n\n## Scene\nA scanned invoice."
	if err := ValidateImageBaselineText(text); err != nil {
		t.Fatalf("ValidateImageBaselineText rejected OCR heading in Text: %v", err)
	}
}

func TestCanonicalContentBlocksRejectUnsupportedBlocks(t *testing.T) {
	for _, block := range []ContentBlock{
		ThinkingContent{Thinking: "private reasoning"},
		ToolCall{ID: "call-1", Name: "example"},
	} {
		err := ValidateCanonicalContentBlocks([]ContentBlock{block})
		if !errors.Is(err, ErrUnsupportedCanonicalBlock) {
			t.Errorf("ValidateCanonicalContentBlocks(%T) error = %v, want ErrUnsupportedCanonicalBlock", block, err)
		}
	}
}

func TestFlattenCanonicalTextUsesStableUnavailableProjection(t *testing.T) {
	blocks := []ContentBlock{TextContent{Text: "before"}, ImageRefContent{MediaID: "id"}}
	want := "before [Image baseline unavailable.]"
	if got := FlattenCanonicalText(blocks); got != want {
		t.Errorf("FlattenCanonicalText = %q, want %q", got, want)
	}
	if err := blocks[1].(ImageRefContent).Validate(); err != nil {
		t.Fatalf("unavailable reference should be valid: %v", err)
	}
}

func TestMarshalContentBlocksSkipsInternalKinds(t *testing.T) {
	data, err := MarshalContentBlocks([]ContentBlock{
		ThinkingContent{Thinking: "secret"},
		ImageRefContent{MediaID: "internal"},
		TextContent{Text: "visible"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	blocks, err := UnmarshalContentBlocks(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %#v, want only the text block", blocks)
	}
}
