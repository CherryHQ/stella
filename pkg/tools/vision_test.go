package tools

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestDetectImageMime(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if got := DetectImageMime(buf.Bytes()); got != "image/png" {
		t.Errorf("png mime = %q, want image/png", got)
	}
	if got := DetectImageMime([]byte("just text, not an image")); got != "" {
		t.Errorf("text mime = %q, want empty", got)
	}
	if got := DetectImageMime(nil); got != "" {
		t.Errorf("nil mime = %q, want empty", got)
	}
}
