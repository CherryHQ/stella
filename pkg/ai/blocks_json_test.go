package ai

import (
	"errors"
	"reflect"
	"strings"
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

func TestCanonicalContentBlocksRejectRawImageAndRoundTripRef(t *testing.T) {
	_, err := MarshalCanonicalContentBlocks([]ContentBlock{ImageContent{Data: "aGVsbG8=", MimeType: "image/png"}})
	if !errors.Is(err, ErrRawImageContent) {
		t.Fatalf("MarshalCanonicalContentBlocks raw image error = %v, want ErrRawImageContent", err)
	}

	in := []ContentBlock{TextContent{Text: "look"}, ImageRefContent{
		MediaID:  "018fbc12-e8dc-7502-916d-46c695202d62",
		MimeType: "image/png",
		Baseline: ImageBaseline{Status: ImageBaselineReady, Text: "## Text\nhello\n\n## Scene\na chart", Renderer: "openai/gpt-4o", Contract: 1},
	}}
	data, err := MarshalCanonicalContentBlocks(in)
	if err != nil {
		t.Fatalf("MarshalCanonicalContentBlocks: %v", err)
	}
	if strings.Contains(string(data), "aGVsbG8=") {
		t.Fatalf("canonical data retained raw bytes: %s", data)
	}
	out, err := UnmarshalContentBlocks(data)
	if err != nil {
		t.Fatalf("UnmarshalContentBlocks: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip = %#v, want %#v", out, in)
	}
}

func TestImageRefRejectsInvalidBaselineContracts(t *testing.T) {
	for _, tt := range []struct {
		name     string
		contract int
		text     string
	}{
		{name: "unknown contract", contract: 999, text: "## Text\nhello\n\n## Scene\na chart"},
		{name: "garbage text", contract: ImageBaselineContractV1, text: "not a baseline"},
		{name: "extra scene section", contract: ImageBaselineContractV1, text: "## Text\nhello\n\n## Scene\na chart\n\n## Details\nmore"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref := ImageRefContent{MediaID: "media", MimeType: "image/png", Baseline: ImageBaseline{
				Status: ImageBaselineReady, Text: tt.text, Renderer: "test/vlm", Contract: tt.contract,
			}}
			if err := ref.Validate(); err == nil {
				t.Fatal("ImageRefContent.Validate unexpectedly accepted invalid baseline")
			}
			if _, err := MarshalCanonicalContentBlocks([]ContentBlock{ref}); err == nil {
				t.Fatal("MarshalCanonicalContentBlocks unexpectedly accepted invalid baseline")
			}
		})
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
		_, err := MarshalCanonicalContentBlocks([]ContentBlock{block})
		if !errors.Is(err, ErrUnsupportedCanonicalBlock) {
			t.Errorf("MarshalCanonicalContentBlocks(%T) error = %v, want ErrUnsupportedCanonicalBlock", block, err)
		}
	}
}

func TestFlattenCanonicalTextUsesStableUnavailableProjection(t *testing.T) {
	blocks := []ContentBlock{TextContent{Text: "before"}, ImageRefContent{
		MediaID: "id", MimeType: "image/png", Baseline: ImageBaseline{Status: ImageBaselineUnavailable},
	}}
	want := "before " + UnavailableImageProjection
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
