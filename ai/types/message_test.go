package types

import "testing"

func TestHasImage(t *testing.T) {
	tests := []struct {
		name   string
		blocks []ContentBlock
		want   bool
	}{
		{"nil", nil, false},
		{"empty", []ContentBlock{}, false},
		{"text only", []ContentBlock{TextContent{Text: "hi"}}, false},
		{"image only", []ContentBlock{ImageContent{Data: "x", MimeType: "image/png"}}, true},
		{"mixed", []ContentBlock{TextContent{Text: "hi"}, ImageContent{Data: "x", MimeType: "image/png"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasImage(tt.blocks); got != tt.want {
				t.Errorf("HasImage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFlattenText(t *testing.T) {
	tests := []struct {
		name   string
		blocks []ContentBlock
		want   string
	}{
		{"nil", nil, ""},
		{"empty", []ContentBlock{}, ""},
		{"single text", []ContentBlock{TextContent{Text: "hello"}}, "hello"},
		{"multiple text", []ContentBlock{TextContent{Text: "a"}, TextContent{Text: "b"}}, "a b"},
		{"mixed with image", []ContentBlock{
			TextContent{Text: "describe"},
			ImageContent{Data: "x", MimeType: "image/png"},
			TextContent{Text: "this"},
		}, "describe this"},
		{"empty text skipped", []ContentBlock{TextContent{Text: ""}, TextContent{Text: "only"}}, "only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FlattenText(tt.blocks); got != tt.want {
				t.Errorf("FlattenText() = %q, want %q", got, tt.want)
			}
		})
	}
}
