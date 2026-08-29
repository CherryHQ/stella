package ai

import (
	"errors"
	"strings"
	"testing"
)

// Text and canonical references round-trip. Raw bytes do not: they have no
// writer any more, so a caller that still holds them loses the block here
// instead of minting a new legacy row. The decoder keeps reading the legacy
// kind, because rows written before canonical media still exist.
func TestContentBlocksJSONRoundTrip(t *testing.T) {
	in := []ContentBlock{
		TextContent{Text: "look at this"},
		ImageRefContent{MediaID: "11111111-1111-1111-1111-111111111111", Baseline: ImageBaseline{Text: "## Text\nsign\n\n## Scene\na street sign"}},
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
		t.Fatalf("blocks = %d, want the text and the reference only", len(out))
	}
	if tc, ok := out[0].(TextContent); !ok || tc.Text != "look at this" {
		t.Errorf("block 0 = %#v, want original text", out[0])
	}
	// The baseline does not round-trip on purpose: it belongs to the media row,
	// so a decoded reference is bare and the reader hydrates it from ctx_media.
	ref, ok := out[1].(ImageRefContent)
	if !ok || ref.MediaID != "11111111-1111-1111-1111-111111111111" || ref.Baseline.Text != "" {
		t.Errorf("block 1 = %#v, want a bare canonical reference", out[1])
	}

	legacy, err := UnmarshalContentBlocks([]byte(`[{"kind":"image","data":"aGVsbG8=","mime_type":"image/png"}]`))
	if err != nil {
		t.Fatalf("unmarshal legacy row: %v", err)
	}
	if len(legacy) != 1 {
		t.Fatalf("legacy blocks = %#v, want the inline image", legacy)
	}
	if ic, ok := legacy[0].(ImageContent); !ok || ic.Data != "aGVsbG8=" || ic.MimeType != "image/png" {
		t.Errorf("legacy block = %#v, want the inline image", legacy[0])
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

// Group history stores canonical references, and only the reference: the
// baseline is a property of the media object, so this codec must neither write
// it nor invent one on the way back. A legacy row that still carries the key
// decodes as if it never had one.
func TestContentBlocksRoundTripImageRefWithoutBaseline(t *testing.T) {
	baseline := ImageBaseline{Text: "## Text\nsign\n\n## Scene\na street sign"}
	data, err := MarshalContentBlocks([]ContentBlock{
		TextContent{Text: "look"},
		ImageRefContent{MediaID: "media-1", Baseline: baseline},
		ImageRefContent{MediaID: "media-2"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "baseline") {
		t.Fatalf("marshalled blocks carry a baseline: %s", data)
	}
	blocks, err := UnmarshalContentBlocks(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("blocks = %#v, want text and two references", blocks)
	}
	for _, index := range []int{1, 2} {
		ref, ok := blocks[index].(ImageRefContent)
		if !ok || ref.Baseline.Text != "" {
			t.Fatalf("reference %d = %#v, want a bare reference", index, blocks[index])
		}
		if ref.Baseline.Projection() != UnavailableImageProjection {
			t.Fatalf("bare projection = %q, want the stable placeholder", ref.Baseline.Projection())
		}
	}

	legacy, err := UnmarshalContentBlocks([]byte(`[{"kind":"image_ref","media_id":"media-1","baseline":"## Text\nsign\n\n## Scene\na street sign"}]`))
	if err != nil {
		t.Fatalf("unmarshal legacy row: %v", err)
	}
	if len(legacy) != 1 {
		t.Fatalf("legacy blocks = %#v, want the reference", legacy)
	}
	if ref, ok := legacy[0].(ImageRefContent); !ok || ref.MediaID != "media-1" || ref.Baseline.Text != "" {
		t.Fatalf("legacy reference = %#v, want the stored baseline key ignored", legacy[0])
	}
}

// A reference with no media ID cannot be hydrated. Decoding must skip it the
// way it skips an unknown kind, never hand a reader a broken reference.
func TestUnmarshalContentBlocksSkipsUnusableImageRef(t *testing.T) {
	blocks, err := UnmarshalContentBlocks([]byte(`[{"kind":"image_ref","media_id":" "},{"kind":"text","text":"kept"}]`))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %#v, want only the text block", blocks)
	}
}
