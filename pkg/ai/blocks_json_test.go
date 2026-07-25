package ai

import "testing"

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
