package tools

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func encodePNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func encodeGIF(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	palette := color.Palette{color.Black, color.White}
	if err := gif.Encode(&buf, image.NewPaletted(image.Rect(0, 0, 2, 2), palette), nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

// webpHeader is the minimal RIFF/WEBP magic http.DetectContentType matches on:
// "RIFF" + 4-byte size + "WEBPVP" (the sniff signature) + padding.
var webpHeader = append([]byte("RIFF\x00\x00\x00\x00WEBPVP"), make([]byte, 8)...)

func TestDetectImageMime(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"png", encodePNG(t), "image/png"},
		{"jpeg", encodeJPEG(t), "image/jpeg"},
		{"gif", encodeGIF(t), "image/gif"},
		{"webp", webpHeader, "image/webp"},
		{"text", []byte("just text, not an image"), ""},
		{"pdf", []byte("%PDF-1.7 binary but not an image"), ""},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectImageMime(tc.data); got != tc.want {
				t.Errorf("DetectImageMime(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}
